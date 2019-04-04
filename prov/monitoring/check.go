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

package monitoring

import (
	"context"
	"fmt"
	"github.com/hashicorp/go-cleanhttp"
	"net"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/pkg/errors"

	"github.com/ystia/yorc/v3/deployments"
	"github.com/ystia/yorc/v3/events"
	"github.com/ystia/yorc/v3/helper/consulutil"
	"github.com/ystia/yorc/v3/log"
	"github.com/ystia/yorc/v3/tosca"
)

// NewCheck allows to instantiate a Check
func NewCheck(deploymentID, nodeName, instance string) *Check {
	return &Check{ID: buildID(deploymentID, nodeName, instance), Report: CheckReport{DeploymentID: deploymentID, NodeName: nodeName, Instance: instance}}
}

// NewCheckFromID allows to instantiate a new Check from an pre-existing ID
func NewCheckFromID(checkID string) (*Check, error) {
	tab := strings.Split(checkID, ":")
	if len(tab) != 3 {
		return nil, errors.Errorf("Malformed check ID :%q", checkID)
	}
	return &Check{ID: checkID, Report: CheckReport{DeploymentID: tab[0], NodeName: tab[1], Instance: tab[2]}}, nil
}

// Start allows to start running a TCP check
func (c *Check) Start() {
	c.stopLock.Lock()
	defer c.stopLock.Unlock()

	// Instantiate ctx for check
	lof := events.LogOptionalFields{
		events.InstanceID: c.Report.Instance,
		events.NodeID:     c.Report.NodeName,
	}
	c.ctx = events.NewContext(context.Background(), lof)

	// timeout is defined arbitrary as half interval to avoid overlap
	c.timeout = c.TimeInterval / 2
	// instantiate channel to close the check ticker
	c.chStop = make(chan struct{})

	c.stop = false
	go c.run()
}

// Stop allows to stop a TCP check
func (c *Check) Stop() {
	c.stopLock.Lock()
	defer c.stopLock.Unlock()

	if !c.stop {
		c.stop = true
		close(c.chStop)
	}
}

func (c *Check) run() {
	log.Debugf("Running check:%+v", c)
	ticker := time.NewTicker(c.TimeInterval)
	for {
		select {
		case <-c.chStop:
			log.Debugf("Stop running check with id:%s", c.ID)
			ticker.Stop()
			return
		case <-ticker.C:
			c.check()
		}
	}
}

func (c *Check) check() {
	if c.CheckType == CheckTypeTCP {
		c.checkTCP()
	} else if c.CheckType == CheckTypeHTTP {
		c.checkHTTP()
	}
}

func (c *Check) checkTCP() {
	tcpAddr := fmt.Sprintf("%s:%d", c.tcpConn.address, c.tcpConn.port)
	conn, err := net.DialTimeout("tcp", tcpAddr, c.timeout)
	if err != nil {
		log.Debugf("[WARN] TCP check (id:%q) connection failed for address:%s", c.ID, tcpAddr)
		c.updateStatus(CheckStatusCRITICAL)
		return
	}
	conn.Close()
	c.updateStatus(CheckStatusPASSING)
}

func (c *Check) checkHTTP() {
	// instantiate httpClient if not already done
	if c.httpConn.httpClient == nil {
		trans := cleanhttp.DefaultTransport()
		trans.DisableKeepAlives = true

		c.httpConn.httpClient = &http.Client{
			Timeout:   c.timeout,
			Transport: trans,
		}
	}

	// Create HTTP Request
	url := fmt.Sprintf("%s://%s:%d", c.httpConn.scheme, c.httpConn.address, c.httpConn.port)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		log.Debugf("[WARN] check HTTP request (id:%q) failed for url:%s", c.ID, url)
		c.updateStatus(CheckStatusCRITICAL)
		return
	}

	// instantiate headers
	if c.httpConn.header == nil {
		c.httpConn.header = make(http.Header)
		for k, v := range c.httpConn.headersMap {
			c.httpConn.header.Add(k, v)
		}

		if c.httpConn.header.Get("Accept") == "" {
			c.httpConn.header.Set("Accept", "text/plain, text/*, */*")
		}
	}
	req.Header = c.httpConn.header

	// Send request
	resp, err := c.httpConn.httpClient.Do(req)
	if err != nil {
		log.Debugf("[WARN] check HTTP request (id:%q) failed for url:%s", c.ID, url)
		c.updateStatus(CheckStatusCRITICAL)
		return
	}
	defer resp.Body.Close()

	// Check response status code
	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		c.updateStatus(CheckStatusPASSING)

	} else if resp.StatusCode == 429 {
		// 429 Too Many Requests (RFC 6585)
		log.Debugf("[WARN] check HTTP request (id:%q) failed for url:%s", c.ID, url)
		c.updateStatus(CheckStatusWARNING)
	} else {
		log.Debugf("[WARN] check HTTP request (id:%q) failed for url:%s", c.ID, url)
		c.updateStatus(CheckStatusCRITICAL)
	}
}

func (c *Check) exist() bool {
	checkPath := path.Join(consulutil.MonitoringKVPrefix, "reports", c.ID, "status")
	KVPair, _, err := defaultMonManager.cc.KV().Get(checkPath, nil)
	if err != nil {
		log.Println("[WARN] Failed to get check due to error:%+v", err)
		return false
	}
	if KVPair == nil || len(KVPair.Value) == 0 {
		return false
	}
	return true
}

func (c *Check) updateStatus(status CheckStatus) {
	if c.Report.Status != status {
		// Be sure check isn't currently being removed before check has been stopped
		if !c.exist() {
			return
		}
		log.Debugf("Update check status from %q to %q", c.Report.Status.String(), status.String())
		err := consulutil.StoreConsulKeyAsString(path.Join(consulutil.MonitoringKVPrefix, "reports", c.ID, "status"), status.String())
		if err != nil {
			log.Printf("[WARN] TCP check updating status failed for check ID:%q due to error:%+v", c.ID, err)
		}
		c.Report.Status = status
		c.notify()
	}
}

func (c *Check) notify() {
	var nodeState tosca.NodeState
	if c.Report.Status == CheckStatusPASSING {
		// Back to normal
		nodeState = tosca.NodeStateStarted
		events.WithContextOptionalFields(c.ctx).NewLogEntry(events.LogLevelINFO, c.Report.DeploymentID).Registerf("Monitoring Check is back to normal for node (%s-%s)", c.Report.NodeName, c.Report.Instance)

	} else if c.Report.Status == CheckStatusCRITICAL {
		// Node in ERROR
		nodeState = tosca.NodeStateError
		events.WithContextOptionalFields(c.ctx).NewLogEntry(events.LogLevelERROR, c.Report.DeploymentID).Registerf("Monitoring Check returned a failure for node (%s-%s)", c.Report.NodeName, c.Report.Instance)
	} else {
		nodeState = tosca.NodeStateError
		events.WithContextOptionalFields(c.ctx).NewLogEntry(events.LogLevelWARN, c.Report.DeploymentID).Registerf("Monitoring Check returned a warning for node (%s-%s)", c.Report.NodeName, c.Report.Instance)
	}

	// Update the node state
	if err := deployments.SetInstanceStateWithContextualLogs(c.ctx, defaultMonManager.cc.KV(), c.Report.DeploymentID, c.Report.NodeName, c.Report.Instance, nodeState); err != nil {
		log.Printf("[WARN] Unable to update node state due to error:%+v", err)
	}
}

func buildID(deploymentID, nodeName, instance string) string {
	return fmt.Sprintf("%s:%s:%s", deploymentID, nodeName, instance)
}
