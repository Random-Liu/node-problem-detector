/*
Copyright 2016 The Kubernetes Authors All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package problemclient

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"

	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/api/unversioned"
	"k8s.io/kubernetes/pkg/client/record"
	"k8s.io/kubernetes/pkg/client/restclient"
	client "k8s.io/kubernetes/pkg/client/unversioned"
	"k8s.io/kubernetes/pkg/types"
	"k8s.io/kubernetes/pkg/util"
	nodeutil "k8s.io/kubernetes/pkg/util/node"
)

var (
	hostnameOverride   = flag.String("hostname-override", "", "If non-empty, will use this string as identification instead of the actual hostname. Default: \"\". ")
	insecureConnection = flag.Bool("insecure-connection", false, "If true, node problem detector will skip TLS verification while connecting with apiserver. Default: false.")
)

// Client is the interface of problem client
type Client interface {
	// GetConditions get all specifiec conditions of current node.
	GetConditions(conditionTypes []api.NodeConditionType) ([]*api.NodeCondition, error)
	// SetConditions set or update conditions of current node.
	SetConditions(conditions []api.NodeCondition) error
	// Eventf reports the event.
	Eventf(eventType string, source, reason, messageFmt string, args ...interface{})
}

type nodeProblemClient struct {
	nodeName  string
	client    *client.Client
	clock     util.Clock
	recorders map[string]record.EventRecorder
	nodeRef   *api.ObjectReference
}

// NewClientOrDie creates a new problem client, panics if error occurs.
func NewClientOrDie() Client {
	c := &nodeProblemClient{clock: util.RealClock{}}
	var cfg *restclient.Config
	if !*insecureConnection {
		var err error
		cfg, err = restclient.InClusterConfig()
		if err != nil {
			panic(err)
		}
	} else {
		cfg = &restclient.Config{
			Host:     getClusterHostOrDie(),
			Insecure: true,
		}
	}
	// TODO(random-liu): Set QPS Limit
	c.client = client.NewOrDie(cfg)
	c.nodeName = nodeutil.GetHostname(*hostnameOverride)
	c.nodeRef = getNodeRef(c.nodeName)
	c.recorders = make(map[string]record.EventRecorder)
	return c
}

func (c *nodeProblemClient) GetConditions(conditionTypes []api.NodeConditionType) ([]*api.NodeCondition, error) {
	node, err := c.client.Nodes().Get(c.nodeName)
	if err != nil {
		return nil, err
	}
	conditions := []*api.NodeCondition{}
	for _, conditionType := range conditionTypes {
		for _, condition := range node.Status.Conditions {
			if condition.Type == conditionType {
				conditions = append(conditions, &condition)
			}
		}
	}
	return conditions, nil
}

func (c *nodeProblemClient) SetConditions(newConditions []api.NodeCondition) error {
	for i := range newConditions {
		// Each time we update the conditions, we update the heart beat time
		newConditions[i].LastHeartbeatTime = unversioned.NewTime(c.clock.Now())
	}
	patch, err := generatePatch(newConditions)
	if err != nil {
		return nil
	}
	return c.client.Patch(api.StrategicMergePatchType).Resource("nodes").Name(c.nodeName).SubResource("status").Body(patch).Do().Error()
}

func (c *nodeProblemClient) Eventf(eventType, source, reason, messageFmt string, args ...interface{}) {
	recorder, found := c.recorders[source]
	if !found {
		// TODO(random-liu): If needed use separate client and QPS limit for event.
		recorder = getEventRecorder(c.client, c.nodeName, source)
		c.recorders[source] = recorder
	}
	recorder.Eventf(c.nodeRef, eventType, reason, messageFmt, args...)
}

// generatePatch generates condition patch
func generatePatch(conditions []api.NodeCondition) ([]byte, error) {
	raw, err := json.Marshal(&conditions)
	if err != nil {
		return nil, err
	}
	return []byte(fmt.Sprintf(`{"status":{"conditions":%s}}`, raw)), nil
}

// getEventRecorder generates a recorder for specific node name and source.
func getEventRecorder(c *client.Client, nodeName, source string) record.EventRecorder {
	eventBroadcaster := record.NewBroadcaster()
	recorder := eventBroadcaster.NewRecorder(api.EventSource{Component: source, Host: nodeName})
	eventBroadcaster.StartRecordingToSink(c.Events(""))
	return recorder
}

func getNodeRef(nodeName string) *api.ObjectReference {
	// TODO(random-liu): Get node to initalize the node reference
	return &api.ObjectReference{
		Kind:      "Node",
		Name:      nodeName,
		UID:       types.UID(nodeName),
		Namespace: "",
	}
}

// getClusterHostOrDie gets the host name from the environment variables KUBERNETES_SERVICE_HOST
// and KUBERNETES_SERVICE_PORT. If any of them is not set, the function will panic.
func getClusterHostOrDie() string {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if len(host) == 0 || len(port) == 0 {
		panic(fmt.Errorf("unable to load in-cluster configuration, KUBERNETES_SERVICE_HOST and KUBERNETES_SERVICE_PORT must be defined"))
	}
	return "https://" + net.JoinHostPort(host, port)
}
