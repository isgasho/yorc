// Copyright 2019 Bull S.A.S. Atos Technologies - Bull, Rue Jean Jaures, B.P.68, 78340, Les Clayes-sous-Bois, France.
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

package kubernetes

import (
	"os"
	"testing"

	"github.com/ystia/yorc/v4/testutil"
)

func TestConsulKubernetesPackage(t *testing.T) {
	cfg := testutil.SetupTestConfig(t)
	srv, _ := testutil.NewTestConsulInstance(t, &cfg)
	defer func() {
		srv.Stop()
		os.RemoveAll(cfg.WorkingDirectory)
	}()

	t.Run("groupK8S", func(t *testing.T) {
		t.Run("testExecutionCancelJob", func(t *testing.T) {
			testExecutionCancelJob(t)
		})
		t.Run("testsController", func(t *testing.T) {
			testsController(t, srv)
		})
		t.Run("testExecutionExecuteInvalidOperation", func(t *testing.T) {
			testExecutionExecuteInvalidOperation(t)
		})
		t.Run("testExecutionGetExpectedInstances", func(t *testing.T) {
			testExecutionGetExpectedInstances(t)
		})
		t.Run("testExecutionManageNamespaceDeletion", func(t *testing.T) {
			testExecutionManageNamespaceDeletion(t)
		})

	})
}
