// Copyright 2018 Bull S.A.S. Atos Technologies - Bull, Rue Jean Jaures, B.P.68, 78340, Les Clayes-sous-Bois, France.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package deployments

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/pkg/errors"
	"github.com/ystia/yorc/v4/events"
	"github.com/ystia/yorc/v4/helper/collections"
	"github.com/ystia/yorc/v4/helper/consulutil"
	"github.com/ystia/yorc/v4/log"
	"github.com/ystia/yorc/v4/tosca"
)

// AttributeData represents the related attribute data
type AttributeData struct {
	DeploymentID     string
	NodeName         string
	InstanceName     string
	Name             string
	Value            string
	CapabilityName   string
	RequirementIndex string
}

// Notifier represents the action of notify it's value change
type Notifier interface {
	NotifyValueChange(ctx context.Context, deploymentID string) error
}

// AttributeNotifier is an attribute notifying its value changes
type AttributeNotifier struct {
	NodeName       string
	InstanceName   string
	AttributeName  string
	CapabilityName string
}

// OperationOutputNotifier is an operation output notifying its value changes
type OperationOutputNotifier struct {
	NodeName      string
	InstanceName  string
	InterfaceName string
	OperationName string
	OutputName    string
}

type notifiedAttribute struct {
	nodeName       string
	instanceName   string
	attributeName  string
	capabilityName string
	deploymentID   string
}

// BuildAttributeDataFromPath allows to return attribute data from path as below:
// - instance attribute:     _yorc/deployments/<DEPLOYMENT_ID>/topology/instances/<NODE_NAME>/<INSTANCE_NAME>/attributes/<ATTRIBUTE_NAME>
// - capability attribute:   _yorc/deployments/<DEPLOYMENT_ID>/topology/instances/<NODE_NAME>/<INSTANCE_NAME>/capabilities/(/*)*/attributes/<ATTRIBUTE_NAME>
// - relationship attribute: _yorc/deployments/<DEPLOYMENT_ID>/topology/relationship_instances/<NODE_NAME>/<REQUIREMENT_INDEX>/<INSTANCE_NAME>/attributes/<ATTRIBUTE_NAME>
func BuildAttributeDataFromPath(ctx context.Context, aPath string) (*AttributeData, error) {
	// Find instance attribute path
	match := regexp.MustCompile(consulutil.DeploymentKVPrefix + "/([0-9a-zA-Z-_]+)/topology/instances/([0-9a-zA-Z-_]+)/([0-9a-zA-Z-]*)/attributes/(\\w+)").FindStringSubmatch(aPath)
	if match != nil && len(match) == 5 {
		return &AttributeData{
			DeploymentID: match[1],
			NodeName:     match[2],
			InstanceName: match[3],
			Name:         match[4],
		}, nil
	}

	// Find capabilities instance attribute path
	match = regexp.MustCompile(consulutil.DeploymentKVPrefix + "/([0-9a-zA-Z-_]+)/topology/instances/([0-9a-zA-Z-_]+)/([0-9a-zA-Z-]*)/capabilities/([/0-9a-zA-Z]+)/attributes/(\\w+)").FindStringSubmatch(aPath)
	if match != nil && len(match) == 6 {
		return &AttributeData{
			DeploymentID:   match[1],
			NodeName:       match[2],
			InstanceName:   match[3],
			CapabilityName: match[4],
			Name:           match[5],
		}, nil
	}

	// Find relationship instance attribute path
	match = regexp.MustCompile(consulutil.DeploymentKVPrefix + "/([0-9a-zA-Z-_]+)/topology/relationship_instances/([0-9a-zA-Z-_]+)/([0-9a-zA-Z-]+)/([/0-9a-zA-Z]*)/attributes/(\\w+)").FindStringSubmatch(aPath)
	if match != nil && len(match) == 6 {
		return &AttributeData{
			DeploymentID:     match[1],
			NodeName:         match[2],
			RequirementIndex: match[3],
			InstanceName:     match[4],
			Name:             match[5],
		}, nil
	}
	return nil, errors.Errorf("failed to build attribute data from path:%q", aPath)
}

// NotifyValueChange allows to notify output value change
func (oon *OperationOutputNotifier) NotifyValueChange(ctx context.Context, deploymentID string) error {
	log.Debugf("Received operation output value change notification for [deploymentID:%q, nodeName:%q, instanceName:%q, interfaceName:%q, operationName:%q, outputName:%q", deploymentID, oon.NodeName, oon.InstanceName, oon.InterfaceName, oon.OperationName, oon.OutputName)
	notificationsPath := path.Join(consulutil.DeploymentKVPrefix, deploymentID, "topology", "instances", oon.NodeName, oon.InstanceName, "outputs", oon.InterfaceName, oon.OperationName, "attribute_notifications", oon.OutputName)
	return notifyAttributeOnValueChange(ctx, notificationsPath, deploymentID)
}

// NotifyValueChange allows to notify attribute value change
func (an *AttributeNotifier) NotifyValueChange(ctx context.Context, deploymentID string) error {
	log.Debugf("Received instance attribute value change notification for [deploymentID:%q, nodeName:%q, instanceName:%q, capabilityName:%q, attributeName:%q", deploymentID, an.NodeName, an.InstanceName, an.CapabilityName, an.AttributeName)
	var notificationsPath string
	if an.CapabilityName != "" {
		notificationsPath = path.Join(consulutil.DeploymentKVPrefix, deploymentID, "topology", "instances", an.NodeName, an.InstanceName, "capabilities", an.CapabilityName, "attribute_notifications", an.AttributeName)
	} else {
		notificationsPath = path.Join(consulutil.DeploymentKVPrefix, deploymentID, "topology", "instances", an.NodeName, an.InstanceName, "attribute_notifications", an.AttributeName)
	}

	return notifyAttributeOnValueChange(ctx, notificationsPath, deploymentID)
}

func notifyAttributeOnValueChange(ctx context.Context, notificationsPath, deploymentID string) error {
	kvs, err := consulutil.List(notificationsPath + "/")
	if err != nil {
		return err
	}
	for _, value := range kvs {

		notified, err := getNotifiedAttribute(string(value))
		log.Debugf("Need to notify attribute:%+v from attribute/operation output value change", notified)
		if err != nil {
			return err
		}
		if notified.capabilityName != "" {
			err = updateInstanceAttributeValue(ctx, deploymentID, notified.nodeName, notified.instanceName, notified.capabilityName, notified.attributeName)
			if err != nil {
				return err
			}
			continue
		}

		err = updateInstanceAttributeValue(ctx, deploymentID, notified.nodeName, notified.instanceName, notified.attributeName)
		if err != nil {
			return err
		}
	}
	return nil
}

func nodeHasAttributeOrCapabilityAttribute(ctx context.Context, deploymentID, nodeName, capabilityName, attributeName string) (bool, error) {
	if capabilityName != "" {
		capabilityType, err := GetNodeCapabilityType(ctx, deploymentID, nodeName, capabilityName)
		if err != nil || capabilityType == "" {
			return false, err
		}
		return TypeHasAttribute(ctx, deploymentID, capabilityType, attributeName, true)
	}
	return NodeHasAttribute(ctx, deploymentID, nodeName, attributeName, true)
}

func addSubstitutionMappingAttributeHostNotification(ctx context.Context, deploymentID, nodeName, instanceName, capabilityName, attributeName string, notifiedAttr *notifiedAttribute) error {
	hasAttribute, err := nodeHasAttributeOrCapabilityAttribute(ctx, deploymentID, nodeName, capabilityName, attributeName)
	if err != nil {
		return err
	}
	if hasAttribute {
		notifier := &AttributeNotifier{
			NodeName:       nodeName,
			InstanceName:   notifiedAttr.instanceName,
			AttributeName:  attributeName,
			CapabilityName: capabilityName,
		}

		log.Debugf("Add substitution attribute %s for %s %s %s with notifier:%+v", attributeName, deploymentID, nodeName, instanceName, notifier)
		err = notifiedAttr.saveNotification(notifier)
		if err != nil {
			return err
		}
	}
	host, err := GetHostedOnNode(ctx, deploymentID, nodeName)
	if err != nil {
		return err
	}
	if host != "" {
		return addSubstitutionMappingAttributeHostNotification(ctx, deploymentID, host, instanceName, capabilityName, attributeName, notifiedAttr)
	}
	return nil
}

func getNotifierForIPAddressAttributeOfAnEndpoint(ctx context.Context, deploymentID, nodeName, instanceName, capabilityName string) (*AttributeNotifier, error) {
	// We need to determine if we should look at the private_address or public_address of the host
	attrName, _, err := getEndpointCapabilitityHostIPAttributeNameAndNetName(ctx, deploymentID, nodeName, capabilityName)
	if err != nil {
		return nil, err
	}

	// Then we retrieve the node name of the host having this attribute
	node, err := resolveHostNotifier(ctx, deploymentID, nodeName, attrName)
	if err != nil {
		return nil, err
	}

	return &AttributeNotifier{
		NodeName:      node,
		InstanceName:  instanceName,
		AttributeName: attrName,
	}, nil
}

func getNotifierForIPAddressAttributeOfACapabilityIfEndpoint(ctx context.Context, deploymentID, nodeName, instanceName, capabilityName, attributeName string) (*AttributeNotifier, error) {
	isEndpointCap, err := isNodeCapabilityOfType(ctx, deploymentID, nodeName, capabilityName, tosca.EndpointCapability)
	if err != nil {
		return nil, err
	}
	if isEndpointCap && attributeName == tosca.EndpointCapabilityIPAddressAttribute {
		return getNotifierForIPAddressAttributeOfAnEndpoint(ctx, deploymentID, nodeName, instanceName, capabilityName)
	}
	return nil, nil
}

func addSubstitutionMappingAttributeNotification(ctx context.Context, deploymentID, nodeName, instanceName, attributeName string) error {
	items := strings.Split(attributeName, ".")
	capabilityName := items[1]
	capAttrName := items[2]

	// Check this capability attribute is really exposed before returning its
	// value
	attributesSet := make(map[string]struct{})
	err := storeSubstitutionMappingAttributeNamesInSet(ctx, deploymentID, nodeName, attributesSet)
	if err != nil {
		return err
	}
	if _, ok := attributesSet[attributeName]; ok {
		notifiedAttr := &notifiedAttribute{
			nodeName:      nodeName,
			deploymentID:  deploymentID,
			instanceName:  instanceName,
			attributeName: attributeName,
		}

		notifier, err := getNotifierForIPAddressAttributeOfACapabilityIfEndpoint(ctx, deploymentID, nodeName, instanceName, capabilityName, capAttrName)
		if err != nil {
			return err
		}
		if notifier != nil {
			log.Debugf("Add substitution attribute %s for %s %s %s with notifier:%+v", attributeName, deploymentID, nodeName, instanceName, notifier)
			return notifiedAttr.saveNotification(notifier)
		}
		// As we can't say if the capability attribute is related to node nodeName or its host, we add notifications for all
		return addSubstitutionMappingAttributeHostNotification(ctx, deploymentID, nodeName, instanceName, capabilityName, capAttrName, notifiedAttr)
	}
	return nil
}

// This allows to store notifications for attributes depending on other ones or on operation outputs  in order to ensure events publication when attribute value change
// This allows too to publish initial state for default attribute value
func addAttributeNotifications(ctx context.Context, deploymentID, nodeName, instanceName, attributeName string) error {
	substitutionInstance, err := isSubstitutionNodeInstance(ctx, deploymentID, nodeName, instanceName)
	if err != nil {
		return err
	}

	// Publish attributes for substitution attributes
	if substitutionInstance {
		found, result := getSubstitutionInstanceAttribute(deploymentID, nodeName, instanceName, attributeName)
		if found {
			events.PublishAndLogAttributeValueChange(context.Background(), deploymentID, nodeName, instanceName, attributeName, result, "updated")
			return nil
		}
	}

	if isSubstitutionMappingAttribute(attributeName) && !substitutionInstance {
		return addSubstitutionMappingAttributeNotification(ctx, deploymentID, nodeName, instanceName, attributeName)
	}

	nodeType, err := GetNodeType(ctx, deploymentID, nodeName)
	if err != nil {
		return err
	}

	// First look at node type as instance values can't contain functions
	node, err := getNodeTemplate(ctx, deploymentID, nodeName)
	if err != nil {
		return err
	}

	va, is := node.Attributes[attributeName]
	if is && va != nil && va.Type != tosca.ValueAssignmentFunction {
		return nil
	}

	// Check type if node is substitutable
	nodeType, err = checkTypeForSubstitutableNode(ctx, deploymentID, nodeName, nodeType)
	if err != nil {
		return err
	}

	// Retrieve related propertyDefinition with default property
	value, isFunction, err := getTypeDefaultAttribute(ctx, deploymentID, nodeType, attributeName)
	if err != nil {
		return err
	}
	// Publish default value
	if value != nil && !isFunction {
		events.PublishAndLogAttributeValueChange(context.Background(), deploymentID, nodeName, instanceName, attributeName, value.String(), "default")
		return nil
	}

	if value == nil {
		// No default found in type hierarchy
		// then traverse HostedOn relationships to find the value
		var host string
		host, err = GetHostedOnNode(ctx, deploymentID, nodeName)
		if err != nil {
			return errors.Wrapf(err, "Failed to add instance attribute notifications %q for node %q (instance %q)", attributeName, nodeName, instanceName)
		}
		if host != "" {
			return addAttributeNotifications(ctx, deploymentID, host, instanceName, attributeName)
		}
	}

	// all possibilities have been checked at this point: check if any get_attribute function is contained
	if value != nil {
		notifiedAttr := &notifiedAttribute{
			deploymentID:  deploymentID,
			nodeName:      nodeName,
			instanceName:  instanceName,
			attributeName: attributeName,
		}
		err = notifiedAttr.parseFunction(ctx, value.RawString())
		if err != nil {
			return err
		}
	}
	// Check if attribute can be updated
	return updateInstanceAttributeValue(ctx, deploymentID, nodeName, instanceName, attributeName)
}

// This is looking for Tosca get_attribute and get_operation_output functions
func (notifiedAttr *notifiedAttribute) parseFunction(ctx context.Context, rawFunction string) error {
	// Function
	f, err := tosca.ParseFunction(rawFunction)
	if err != nil {
		return err
	}
	log.Debugf("function = %+v", f)
	fcts := f.GetFunctionsByOperator(tosca.GetAttributeOperator)
	for _, fct := range fcts {
		// Find related notifier
		operands := make([]string, len(fct.Operands))
		for i, op := range fct.Operands {
			operands[i] = op.String()
		}
		notifier, err := notifiedAttr.findAttributeNotifier(ctx, operands)
		if err != nil {
			return errors.Wrapf(err, "Failed to find get_attribute notifier for function: %q and node %q", fct, notifiedAttr.nodeName)
		}

		// Store notification
		err = notifiedAttr.saveNotification(notifier)
		if err != nil {
			return errors.Wrapf(err, "Failed to save notification from notifier:%+v and notified %+v", notifier, notifiedAttr)
		}
	}

	fcts = f.GetFunctionsByOperator(tosca.GetOperationOutputOperator)
	for _, fct := range fcts {
		// Find related notifier
		operands := make([]string, len(fct.Operands))
		for i, op := range fct.Operands {
			operands[i] = op.String()
		}
		notifier, err := notifiedAttr.findOperationOutputNotifier(operands)
		if err != nil {
			return errors.Wrapf(err, "Failed to find get_attribute notifier for function: %q and node %q", fct, notifiedAttr.nodeName)
		}

		// Store notification
		err = notifiedAttr.saveNotification(notifier)
		if err != nil {
			return errors.Wrapf(err, "Failed to save notification from notifier:%+v and notified %+v", notifier, notifiedAttr)
		}
	}
	return nil
}

func (notifiedAttr *notifiedAttribute) findOperationOutputNotifier(operands []string) (Notifier, error) {
	funcString := fmt.Sprintf("get_operation_output: [%s]", strings.Join(operands, ", "))
	if len(operands) != 4 {
		return nil, errors.Errorf("expecting exactly four parameters for a get_operation_output function (%s)", funcString)
	}
	return &OperationOutputNotifier{
		InstanceName:  notifiedAttr.instanceName,
		NodeName:      notifiedAttr.nodeName,
		OperationName: strings.ToLower(operands[2]),
		InterfaceName: strings.ToLower(operands[1]),
		OutputName:    operands[3],
	}, nil
}

func (notifiedAttr *notifiedAttribute) findAttributeNotifier(ctx context.Context, operands []string) (Notifier, error) {
	funcString := fmt.Sprintf("get_attribute: [%s]", strings.Join(operands, ", "))
	var node, capName, attrName string
	var err error

	if len(operands) == 2 {
		attrName = operands[1]
	} else if len(operands) == 3 {
		attrName = operands[2]
		capName = operands[1]
	} else {
		return nil, errors.Errorf("expecting two or three parameters for a non-relationship context get_attribute function (%s)", funcString)
	}

	switch operands[0] {
	case funcKeywordSELF:
		// Default case is to look at the node name (easy!)
		node = notifiedAttr.nodeName
		// Now check for the famous ip_address attribute of any capabilities derived from tosca.capabilities.Endpoint
		// http://docs.oasis-open.org/tosca/TOSCA-Simple-Profile-YAML/v1.2/TOSCA-Simple-Profile-YAML-v1.2.html#DEFN_TYPE_CAPABILITIES_ENDPOINT
		// is it should come from the host (this is the hard way!)
		if capName != "" {
			// Is this an endpoint?
			notifier, err := getNotifierForIPAddressAttributeOfACapabilityIfEndpoint(ctx, notifiedAttr.deploymentID, notifiedAttr.nodeName, notifiedAttr.instanceName, capName, attrName)
			if err != nil || notifier != nil {
				return notifier, err
			}
		}

	case funcKeywordHOST:
		node, err = resolveHostNotifier(ctx, notifiedAttr.deploymentID, notifiedAttr.nodeName, attrName)
		if err != nil {
			return nil, err
		}
	default:
		return nil, errors.Errorf("unexpected keyword:%q in get_attribute function (%s)", operands[0], funcString)
	}

	if node == "" {
		return nil, errors.Errorf("unable to find node name related to get_attribute function (%s)", funcString)
	}

	notifier := &AttributeNotifier{
		NodeName:       node,
		InstanceName:   notifiedAttr.instanceName,
		AttributeName:  attrName,
		CapabilityName: capName,
	}
	return notifier, nil
}

func (notifiedAttr *notifiedAttribute) saveNotification(notifier Notifier) error {
	var notificationsPath string
	switch n := notifier.(type) {
	case *AttributeNotifier:
		if n.CapabilityName != "" {
			notificationsPath = path.Join(consulutil.DeploymentKVPrefix, notifiedAttr.deploymentID, "topology", "instances", n.NodeName, notifiedAttr.instanceName, "capabilities", n.CapabilityName, "attribute_notifications", n.AttributeName)

		} else {
			notificationsPath = path.Join(consulutil.DeploymentKVPrefix, notifiedAttr.deploymentID, "topology", "instances", n.NodeName, notifiedAttr.instanceName, "attribute_notifications", n.AttributeName)
		}
	case *OperationOutputNotifier:
		notificationsPath = path.Join(consulutil.DeploymentKVPrefix, notifiedAttr.deploymentID, "topology", "instances", n.NodeName, n.InstanceName, "outputs", n.InterfaceName, n.OperationName, "attribute_notifications", n.OutputName)

	default:
		return errors.Errorf("Unexpected type %T for saving notifications", n)
	}

	notifs, err := consulutil.GetKeys(notificationsPath)
	if err != nil {
		return err
	}
	var index int
	if notifs != nil {
		index = len(notifs)
	}

	key := path.Join(notificationsPath, strconv.Itoa(index))
	val := buildNotificationValue(notifiedAttr.nodeName, notifiedAttr.instanceName, notifiedAttr.capabilityName, notifiedAttr.attributeName)
	log.Debugf("store notification with[key=%q, value:%q", key, val)
	return consulutil.StoreConsulKeyAsString(key, val)
}

// notification value is path-based as: "<NODE_NAME>/<INSTANCE_NAME>/attributes/<ATTRIBUTE_NAME>
// or "<NODE_NAME>/<INSTANCE_NAME>/capabilities/<CAPABILITY_NAME>/attributes/<ATTRIBUTE_NAME>"
func buildNotificationValue(nodeName, instanceName, capabilityName, attributeName string) string {
	if capabilityName != "" {
		return path.Join(nodeName, instanceName, "capabilities", capabilityName, "attributes", attributeName)
	}
	return path.Join(nodeName, instanceName, "attributes", attributeName)
}

func getNotifiedAttribute(notification string) (*notifiedAttribute, error) {
	notified := strings.Split(notification, "/")
	if len(notified) != 4 && len(notified) != 6 {
		return nil, errors.Errorf("unexpected format %q for notification", notification)
	}
	var attribData *notifiedAttribute
	if len(notified) == 4 {
		attribData = &notifiedAttribute{
			nodeName:      notified[0],
			instanceName:  notified[1],
			attributeName: notified[3],
		}
	} else {
		attribData = &notifiedAttribute{
			nodeName:       notified[0],
			instanceName:   notified[1],
			capabilityName: notified[3],
			attributeName:  notified[5],
		}
	}
	return attribData, nil
}

// resolveHostNotifier retrieves the node name hosting the provided nodeName having the provided attributeName in the "HostedOn" relationship stack
// If no host node is found with the related attributeName, root hosting node (compute) is returned as attribute can not be defined in Tosca (as public_ip_address for compatibility)
func resolveHostNotifier(ctx context.Context, deploymentID, nodeName, attributeName string) (string, error) {
	hostNode, err := GetHostedOnNode(ctx, deploymentID, nodeName)
	if err != nil {
		return "", err
	}
	if hostNode == "" {
		return nodeName, nil
	}
	attributes, err := GetNodeAttributesNames(ctx, deploymentID, hostNode)
	if err != nil {
		return "", err
	}
	if collections.ContainsString(attributes, attributeName) {
		return hostNode, nil
	}

	return resolveHostNotifier(ctx, deploymentID, hostNode, attributeName)
}
