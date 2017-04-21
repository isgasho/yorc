package workflow

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/hashicorp/consul/api"
	"github.com/pkg/errors"
	"novaforge.bull.com/starlings-janus/janus/config"
	"novaforge.bull.com/starlings-janus/janus/deployments"
	"novaforge.bull.com/starlings-janus/janus/events"
	"novaforge.bull.com/starlings-janus/janus/helper/consulutil"
	"novaforge.bull.com/starlings-janus/janus/log"
	"novaforge.bull.com/starlings-janus/janus/prov/kubernetes"
	"novaforge.bull.com/starlings-janus/janus/prov/terraform"
	"novaforge.bull.com/starlings-janus/janus/tasks"
	"novaforge.bull.com/starlings-janus/janus/tosca"
)

const wfDelegateActivity string = "delegate"
const wfSetStateActivity string = "set-state"
const wfCallOpActivity string = "call-operation"

type step struct {
	Name       string
	Node       string
	Activities []activity
	Next       []*step
	Previous   []*step
	NotifyChan chan struct{}
	kv         *api.KV
	stepPrefix string
	t          *task
}

type activity interface {
	ActivityType() string
	ActivityValue() string
}

type delegateActivity struct {
	delegate string
}

func (da delegateActivity) ActivityType() string {
	return wfDelegateActivity
}
func (da delegateActivity) ActivityValue() string {
	return da.delegate
}

type setStateActivity struct {
	state string
}

func (s setStateActivity) ActivityType() string {
	return wfSetStateActivity
}
func (s setStateActivity) ActivityValue() string {
	return s.state
}

type callOperationActivity struct {
	operation string
}

func (c callOperationActivity) ActivityType() string {
	return wfCallOpActivity
}
func (c callOperationActivity) ActivityValue() string {
	return c.operation
}

func (s *step) IsTerminal() bool {
	return len(s.Next) == 0
}

func (s *step) SetTaskID(taskID *task) {
	s.t = taskID
}

func (s *step) notifyNext() {
	for _, next := range s.Next {
		log.Debugf("Step %q, notifying step %q", s.Name, next.Name)
		next.NotifyChan <- struct{}{}
	}
}

type visitStep struct {
	refCount int
	s        *step
}

func (s *step) setStatus(status string) error {
	kvp := &api.KVPair{Key: path.Join(s.stepPrefix, "status"), Value: []byte(status)}
	_, err := s.kv.Put(kvp, nil)
	if err != nil {
		return err
	}
	kvp = &api.KVPair{Key: path.Join(consulutil.WorkflowsPrefix, s.t.ID, s.Name), Value: []byte(status)}
	_, err = s.kv.Put(kvp, nil)
	return err
}

func (s *step) isRelationshipTargetNodeRelated() (bool, error) {
	for _, activity := range s.Activities {
		if activity.ActivityType() == wfCallOpActivity {
			operation := activity.ActivityValue()
			isRelationshipOp, operationRealName, _, targetNode, err := deployments.DecodeOperation(s.kv, s.t.TargetID, s.Node, operation)
			if err != nil {
				return false, err
			}
			if isRelationshipOp {
				if deployments.IsRelationshipOperationOnTargetNode(operationRealName) {
					isNodeTargetTask, err := tasks.IsTaskRelatedNode(s.kv, s.t.ID, targetNode)
					if err != nil || isNodeTargetTask {
						return isNodeTargetTask, err
					}
				}
			}
		}
	}
	return false, nil
}

// isRunnable Checks if a step should be run or bypassed
//
// It first checks if the step is not already done in this workflow instance
// And for ScaleUp and ScaleDown it checks if the node or the target node in case of an operation running on the target node is part of the operation
func (s *step) isRunnable() (bool, error) {

	kvp, _, err := s.kv.Get(path.Join(consulutil.WorkflowsPrefix, s.t.ID, s.Name), nil)
	if err != nil {
		return false, errors.Wrap(err, consulutil.ConsulGenericErrMsg)
	}

	if kvp != nil && string(kvp.Value) == "done" {
		return false, nil
	}

	if s.t.TaskType == tasks.ScaleUp || s.t.TaskType == tasks.ScaleDown {
		isNodeTargetTask, err := tasks.IsTaskRelatedNode(s.kv, s.t.ID, s.Node)
		if err != nil {
			return false, err
		}
		if !isNodeTargetTask {
			isTargetNodeRelated, err := s.isRelationshipTargetNodeRelated()
			if err != nil || !isTargetNodeRelated {
				return false, err
			}
		}
	}

	return true, nil
}

func setNodeStatus(kv *api.KV, eventPub events.Publisher, taskID, deploymentID, nodeName, status string) error {
	instancesIDs, err := tasks.GetInstances(kv, taskID, deploymentID, nodeName)
	if err != nil {
		return err
	}

	for _, id := range instancesIDs {
		// Publish status change event
		err := deployments.SetInstanceStateString(kv, deploymentID, nodeName, id, status)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *step) run(ctx context.Context, deploymentID string, kv *api.KV, ignoredErrsChan chan error, shutdownChan chan struct{}, cfg config.Configuration, bypassErrors bool) error {
	haveErr := false
	eventPub := events.NewPublisher(kv, deploymentID)
	for i := 0; i < len(s.Previous); i++ {
		// Wait for previous be done
		log.Debugf("Step %q waiting for %d previous steps", s.Name, len(s.Previous)-i)
		for {
			select {
			case <-s.NotifyChan:
				log.Debugf("Step %q caught a notification", s.Name)
				goto BR
			case <-shutdownChan:
				log.Printf("Step %q canceled", s.Name)
				s.setStatus("canceled")
				return errors.New("workflow canceled")
			case <-ctx.Done():
				log.Printf("Step %q canceled: %q", s.Name, ctx.Err())
				s.setStatus("canceled")
				return ctx.Err()
			case <-time.After(30 * time.Second):
				log.Debugf("Step %q still waiting for %d previous steps", s.Name, len(s.Previous)-i)
			}
		}
	BR:
	}

	if runnable, err := s.isRunnable(); err != nil {
		return err
	} else if !runnable {
		log.Printf("Deployment %q: Skipping Step %q", deploymentID, s.Name)
		events.LogEngineMessage(kv, deploymentID, fmt.Sprintf("Skipping Step %q", s.Name))
		s.setStatus("done")
		s.notifyNext()
		return nil
	}

	log.Printf("Processing step %q", s.Name)
	for _, activity := range s.Activities {
		actType := activity.ActivityType()
		switch {
		case actType == wfDelegateActivity:
			provisioner := terraform.NewExecutor()
			delegateOp := activity.ActivityValue()
			if err := provisioner.ExecDelegate(ctx, kv, cfg, s.t.ID, deploymentID, s.Node, delegateOp); err != nil {
				setNodeStatus(kv, eventPub, s.t.ID, deploymentID, s.Node, tosca.NodeStateError.String())
				log.Printf("Deployment %q, Step %q: Sending error %v to error channel", deploymentID, s.Name, err)
				s.setStatus(tosca.NodeStateError.String())
				if bypassErrors {
					ignoredErrsChan <- err
					haveErr = true
				} else {
					return err
				}
			}
		case actType == wfSetStateActivity:
			setNodeStatus(kv, eventPub, s.t.ID, deploymentID, s.Node, activity.ActivityValue())
		case actType == wfCallOpActivity:
			exec := kubernetes.NewExecutor()
			err := exec.ExecOperation(ctx, kv, cfg, s.t.ID, deploymentID, s.Node, activity.ActivityValue())
			if err != nil {
				setNodeStatus(kv, eventPub, s.t.ID, deploymentID, s.Node, tosca.NodeStateError.String())
				log.Printf("Deployment %q, Step %q: Sending error %v to error channel", deploymentID, s.Name, err)
				if bypassErrors {
					ignoredErrsChan <- err
					haveErr = true
				} else {
					s.setStatus(tosca.NodeStateError.String())
					return err
				}
			}
		}
	}

	s.notifyNext()

	if haveErr {
		log.Debug("Step %s generate an error but workflow continue", s.Name)
		s.setStatus(tosca.NodeStateError.String())
		// Return nil otherwise the rest of the workflow will be canceled
		return nil
	}
	log.Debugf("Step %s done without error.", s.Name)
	s.setStatus("done")
	return nil
}

func readStep(kv *api.KV, stepsPrefix, stepName string, visitedMap map[string]*visitStep) (*step, error) {
	stepPrefix := stepsPrefix + stepName
	s := &step{Name: stepName, kv: kv, stepPrefix: stepPrefix}
	kvPair, _, err := kv.Get(stepPrefix+"/node", nil)
	if err != nil {
		log.Print(err)
		return nil, err
	}
	if kvPair == nil {
		return nil, fmt.Errorf("Missing node attribute for step %s", stepName)
	}
	s.Node = string(kvPair.Value)

	kvPairs, _, err := kv.List(stepPrefix+"/activity", nil)
	if err != nil {
		return nil, err
	}
	if len(kvPairs) == 0 {
		return nil, fmt.Errorf("Activity missing for step %s, this is not allowed.", stepName)
	}
	s.Activities = make([]activity, 0)
	for _, actKV := range kvPairs {
		key := strings.TrimPrefix(actKV.Key, stepPrefix+"/activity/")
		switch {
		case key == wfDelegateActivity:
			s.Activities = append(s.Activities, delegateActivity{delegate: string(actKV.Value)})
		case key == wfSetStateActivity:
			s.Activities = append(s.Activities, setStateActivity{state: string(actKV.Value)})
		case key == wfCallOpActivity:
			s.Activities = append(s.Activities, callOperationActivity{operation: string(actKV.Value)})
		default:
			return nil, fmt.Errorf("Unsupported activity type: %s", key)
		}
	}
	s.NotifyChan = make(chan struct{})

	kvPairs, _, err = kv.List(stepPrefix+"/next", nil)
	if err != nil {
		return nil, err
	}
	s.Next = make([]*step, 0)
	s.Previous = make([]*step, 0)
	for _, nextKV := range kvPairs {
		var nextStep *step
		nextStepName := strings.TrimPrefix(nextKV.Key, stepPrefix+"/next/")
		if visitStep, ok := visitedMap[nextStepName]; ok {
			log.Debugf("Found existing step %s", nextStepName)
			nextStep = visitStep.s
		} else {
			log.Debugf("Reading new step %s from Consul", nextStepName)
			nextStep, err = readStep(kv, stepsPrefix, nextStepName, visitedMap)
			if err != nil {
				return nil, err
			}
		}

		s.Next = append(s.Next, nextStep)
		nextStep.Previous = append(nextStep.Previous, s)
		visitedMap[nextStepName].refCount++
		log.Debugf("RefCount for step %s set to %d", nextStepName, visitedMap[nextStepName].refCount)
	}
	visitedMap[stepName] = &visitStep{refCount: 0, s: s}
	return s, nil

}

// Creates a workflow tree from values stored in Consul at the given prefix.
// It returns roots (starting) Steps.
func readWorkFlowFromConsul(kv *api.KV, wfPrefix string) ([]*step, error) {
	stepsPrefix := wfPrefix + "/steps/"
	stepsPrefixes, _, err := kv.Keys(stepsPrefix, "/", nil)
	if err != nil {
		log.Print(err)
		return nil, err
	}
	steps := make([]*step, 0)
	visitedMap := make(map[string]*visitStep, len(stepsPrefixes))
	for _, stepPrefix := range stepsPrefixes {
		stepName := path.Base(stepPrefix)
		if visitStep, ok := visitedMap[stepName]; !ok {
			step, err := readStep(kv, stepsPrefix, stepName, visitedMap)
			if err != nil {
				return nil, err
			}
			steps = append(steps, step)
		} else {
			steps = append(steps, visitStep.s)
		}
	}

	return steps, nil
}