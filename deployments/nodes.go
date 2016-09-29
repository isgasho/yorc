package deployments

import (
	"fmt"
	"github.com/hashicorp/consul/api"
	"novaforge.bull.com/starlings-janus/janus/log"
	"path"
	"strconv"
)

// IsNodeTypeDerivedFrom traverses 'derived_from' to check if type derives from another type
func IsNodeTypeDerivedFrom(kv *api.KV, deploymentId, nodeType, derives string) (bool, error) {
	if nodeType == derives {
		return true, nil
	}
	nodeTypePath := path.Join(DeploymentKVPrefix, deploymentId, "topology", "types", nodeType)
	// Check if node type exist
	if kvps, _, err := kv.List(nodeTypePath+"/", nil); err != nil {
		return false, err
	} else if kvps == nil || len(kvps) == 0 {
		return false, fmt.Errorf("Looking for a node type %q that do not exists in deployment %q.", nodeType, deploymentId)
	}

	kvp, _, err := kv.Get(nodeTypePath+"/derived_from", nil)
	if err != nil {
		return false, err
	}
	if kvp == nil || len(kvp.Value) == 0 {
		// This is a root type
		return false, nil
	}
	if string(kvp.Value) == derives {
		// Found it
		return true, nil
	}
	return IsNodeTypeDerivedFrom(kv, deploymentId, string(kvp.Value), derives)
}

// GetNbInstancesForNode retrieves the number of instances for a given node nodeName in deployment deploymentId.
//
// If the node is or is derived from 'tosca.nodes.Compute' it will look for a property 'default_instances' in the 'scalable' capability of
// this node. Otherwise it will search for any relationship derived from 'tosca.relationships.HostedOn' in node requirements and reiterate
// the process. If a Compute is finally found it returns 'true' and the instances number.
// If there is no 'tosca.nodes.Compute' at the end of the hosted on chain then assume that there is only one instance and return 'false'
func GetNbInstancesForNode(kv *api.KV, deploymentId, nodeName string) (bool, uint32, error) {
	nodePath := path.Join(DeploymentKVPrefix, deploymentId, "topology", "nodes", nodeName)
	kvp, _, err := kv.Get(nodePath+"/type", nil)
	if err != nil {
		return false, 0, err
	}
	if kvp == nil || len(kvp.Value) == 0 {
		return false, 0, fmt.Errorf("Missing type for node %q, in deployment %q", nodeName, deploymentId)
	}
	nodeType := string(kvp.Value)
	if ok, err := IsNodeTypeDerivedFrom(kv, deploymentId, nodeType, "tosca.nodes.Compute"); err != nil {
		return false, 0, err
	} else if ok {
		//For now we look into default instances in scalable capability but it will be dynamic at runtime we will have to store the
		//current number of instances somewhere else
		kvp, _, err = kv.Get(nodePath+"/capabilities/scalable/properties/default_instances", nil)
		if err != nil {
			return false, 0, err
		}
		if kvp == nil || len(kvp.Value) == 0 {
			log.Debugf("Missing property 'default_instances' of 'scalable' capability for node %q derived from 'tosca.nodes.Compute', in deployment %q. Lets assume that it is 1.", nodeName, deploymentId)
			return true, 1, nil
		}
		if val, err := strconv.ParseUint(string(kvp.Value), 10, 32); err != nil {
			return false, 0, fmt.Errorf("Not a valid integer for property 'default_instances' of 'scalable' capability for node %q derived from 'tosca.nodes.Compute', in deployment %q. Error: %v", nodeName, deploymentId, err)
		} else {
			return true, uint32(val), nil
		}
	}
	// So we have to traverse the hosted on relationships...
	// Lets inspect the requirements to found hosted on relationships
	hostNode, err := GetHostedOnNode(kv, deploymentId, nodeName)
	if err != nil {
		return false, 0, err
	} else if hostNode != "" {
		return GetNbInstancesForNode(kv, deploymentId, hostNode)
	}
	// Not hosted on a tosca.nodes.Compute assume one instance
	return false, 1, nil
}

// GetNodeInstancesIds returns the names of the different instances for a given node.
//
// It may be an empty array if the given node is not HostedOn a scalable node.
func GetNodeInstancesIds(kv *api.KV, deploymentId, nodeName string) ([]string, error) {
	names := make([]string, 0)
	instancesPath := path.Join(DeploymentKVPrefix, deploymentId, "topology/instances", nodeName)
	instances, _, err := kv.Keys(instancesPath+"/", "/", nil)
	if err != nil {
		return names, err
	}
	for _, instance := range instances {
		names = append(names, path.Base(instance))
	}
	return names, nil
}

// GetNodeInstancesNames returns the node name of the node defined in the first found relationship derived from "tosca.relationships.HostedOn"
//
// If there is no HostedOn relationship for this node then it returns an empty string
func GetHostedOnNode(kv *api.KV, deploymentId, nodeName string) (string, error) {
	nodePath := path.Join(DeploymentKVPrefix, deploymentId, "topology", "nodes", nodeName)
	// So we have to traverse the hosted on relationships...
	// Lets inspect the requirements to found hosted on relationships
	reqKVPs, _, err := kv.Keys(path.Join(nodePath, "requirements")+"/", "/", nil)
	log.Debugf("Deployment: %q. Node %q. Requirements %v", deploymentId, nodeName, reqKVPs)
	if err != nil {
		return "", err
	}
	for _, reqKey := range reqKVPs {
		log.Debugf("Deployment: %q. Node %q. Inspecting requirement %q", deploymentId, nodeName, reqKey)
		// Check requirement relationship
		kvp, _, err := kv.Get(path.Join(reqKey, "relationship"), nil)
		if err != nil {
			return "", err
		}
		if kvp == nil || len(kvp.Value) == 0 {
			return "", fmt.Errorf("Missing 'relationship' attribute for requirement %q for node %q in deployement %q", path.Base(reqKey), nodeName, deploymentId)
		}
		// Is this relationship an HostedOn?
		if ok, err := IsNodeTypeDerivedFrom(kv, deploymentId, string(kvp.Value), "tosca.relationships.HostedOn"); err != nil {
			return "", err
		} else if ok {
			// An HostedOn! Great! let inspect the target node.
			kvp, _, err := kv.Get(path.Join(reqKey, "node"), nil)
			if err != nil {
				return "", err
			}
			if kvp == nil || len(kvp.Value) == 0 {
				return "", fmt.Errorf("Missing 'node' attribute for requirement %q for node %q in deployement %q", path.Base(reqKey), nodeName, deploymentId)
			}
			return string(kvp.Value), nil
		}
	}
	return "", nil
}

// GetTypeDefaultProperty checks if a type has a default value for a given property.
//
// It returns true if a default value is found false otherwise as first return parameter.
// If no default value is found in a given type then the derived_from hierarchy is explored to find the default value.
func GetTypeDefaultProperty(kv *api.KV, deploymentId, typeName, propertyName string) (bool, string, error) {
	return getTypeDefaultAttributeOrProperty(kv, deploymentId, typeName, propertyName, true)
}

// GetTypeDefaultAttribute checks if a type has a default value for a given attribute.
//
// It returns true if a default value is found false otherwise as first return parameter.
// If no default value is found in a given type then the derived_from hierarchy is explored to find the default value.
func GetTypeDefaultAttribute(kv *api.KV, deploymentId, typeName, attributeName string) (bool, string, error) {
	return getTypeDefaultAttributeOrProperty(kv, deploymentId, typeName, attributeName, false)
}

// GetNodeProperty retrieves the value for a given property in a given node
//
// It returns true if a value is found false otherwise as first return parameter.
// If the property is not found in the node then the type hierarchy is explored to find a default value.
// If the property is still not found then it will explore the HostedOn hierarchy.
func GetNodeProperty(kv *api.KV, deploymentId, nodeName, propertyName string) (bool, string, error) {
	nodePath := path.Join(DeploymentKVPrefix, deploymentId, "topology", "nodes", nodeName)
	kvp, _, err := kv.Get(path.Join(nodePath, "properties", propertyName), nil)
	if err != nil {
		return false, "", err
	}
	if kvp != nil {
		return true, string(kvp.Value), nil
	}

	// Not found look at node type
	kvp, _, err = kv.Get(path.Join(nodePath, "type"), nil)
	if err != nil {
		return false, "", err
	}
	if kvp == nil || len(kvp.Value) == 0 {
		return false, "", fmt.Errorf("Missing type for node %q in deployment %q", nodeName, deploymentId)
	}

	ok, value, err := GetTypeDefaultProperty(kv, deploymentId, string(kvp.Value), propertyName)
	if err != nil {
		return false, "", nil
	}
	if ok {
		return true, value, nil
	}
	// No default found in type hierarchy
	// then traverse HostedOn relationships to find the value
	host, err := GetHostedOnNode(kv, deploymentId, nodeName)
	if err != nil {
		return false, "", err
	}
	if host != "" {
		return GetNodeProperty(kv, deploymentId, host, propertyName)
	}
	// Not found anywhere
	return false, "", nil
}

// GetNodeProperty retrieves the values for a given attribute in a given node.
//
// As a node may have multiple instances and attributes may be instance-scoped, then returned result is a map with the instance name as key
// and the retrieved attributes as values.
//
// It returns true if a value is found false otherwise as first return parameter.
// If the property is not found in the node then the type hierarchy is explored to find a default value.
// If the property is still not found then it will explore the HostedOn hierarchy.
func GetNodeAttributes(kv *api.KV, deploymentId, nodeName, attributeName string) (found bool, attributes map[string]string, err error) {
	found = false
	instances, err := GetNodeInstancesIds(kv, deploymentId, nodeName)
	if err != nil {
		return
	}

	if len(instances) > 0 {
		attributes = make(map[string]string)
		nodeInstancesPath := path.Join(DeploymentKVPrefix, deploymentId, "topology", "instances", nodeName)
		for _, instance := range instances {
			var kvp *api.KVPair
			kvp, _, err = kv.Get(path.Join(nodeInstancesPath, instance, "attributes", attributeName), nil)
			if err != nil {
				return
			}
			if kvp != nil {
				attributes[instance] = string(kvp.Value)
			}
		}
		if len(attributes) > 0 {
			found = true
			return
		}
	}

	// Look at not instance-scoped attribute
	nodePath := path.Join(DeploymentKVPrefix, deploymentId, "topology", "nodes", nodeName)

	kvp, _, err := kv.Get(path.Join(nodePath, "attributes", attributeName), nil)
	if err != nil {
		return
	}
	if kvp != nil {
		if attributes == nil {
			attributes = make(map[string]string)
		}
		if len(instances) > 0 {
			for _, instance := range instances {
				attributes[instance] = string(kvp.Value)
			}
		} else {
			attributes[""] = string(kvp.Value)
		}
		found = true
		return
	}

	// Not found look at node type
	kvp, _, err = kv.Get(path.Join(nodePath, "type"), nil)
	if err != nil {
		return
	}
	if kvp == nil || len(kvp.Value) == 0 {
		err = fmt.Errorf("Missing type for node %q in deployment %q", nodeName, deploymentId)
		return
	}

	ok, defaultValue, err := GetTypeDefaultAttribute(kv, deploymentId, string(kvp.Value), attributeName)
	if err != nil {
		return
	}
	if ok {
		if attributes == nil {
			attributes = make(map[string]string)
		}
		if len(instances) > 0 {
			for _, instance := range instances {
				attributes[instance] = string(defaultValue)
			}
		} else {
			attributes[""] = string(defaultValue)
		}
		found = true
		return
	}
	// No default found in type hierarchy
	// then traverse HostedOn relationships to find the value
	var host string
	host, err = GetHostedOnNode(kv, deploymentId, nodeName)
	if err != nil {
		return
	}
	if host != "" {
		return GetNodeAttributes(kv, deploymentId, host, attributeName)
	}
	// Not found anywhere
	return
}

// getTypeDefaultProperty checks if a type has a default value for a given property or attribute.
// It returns true if a default value is found false otherwise as first return parameter.
// If no default value is found in a given type then the derived_from hierarchy is explored to find the default value.
func getTypeDefaultAttributeOrProperty(kv *api.KV, deploymentId, typeName, propertyName string, isProperty bool) (bool, string, error) {
	typePath := path.Join(DeploymentKVPrefix, deploymentId, "topology", "types", typeName)
	var defaultPath string
	if isProperty {
		defaultPath = path.Join(typePath, "properties", propertyName, "default")
	} else {
		defaultPath = path.Join(typePath, "attributes", propertyName, "default")
	}
	kvp, _, err := kv.Get(defaultPath, nil)
	if err != nil {
		return false, "", err
	}
	if kvp != nil {
		return true, string(kvp.Value), nil
	}
	// No default in this type
	// Lets look at parent type
	kvp, _, err = kv.Get(typePath+"/derived_from", nil)
	if err != nil {
		return false, "", err
	}
	if kvp == nil || len(kvp.Value) == 0 {
		return false, "", nil
	}
	return getTypeDefaultAttributeOrProperty(kv, deploymentId, string(kvp.Value), propertyName, isProperty)
}

// GetNodes returns the names of the different nodes for a given deployment.
func GetNodes(kv *api.KV, deploymentId string) ([]string, error) {
	names := make([]string, 0)
	nodesPath := path.Join(DeploymentKVPrefix, deploymentId, "topology/nodes")
	nodes, _, err := kv.Keys(nodesPath+"/", "/", nil)
	if err != nil {
		return names, err
	}
	for _, node := range nodes {
		names = append(names, path.Base(node))
	}
	return names, nil
}