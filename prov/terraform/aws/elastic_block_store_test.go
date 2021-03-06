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

package aws

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/ystia/yorc/v4/config"
	"github.com/ystia/yorc/v4/prov/terraform/commons"
)

func testSimpleEBS(t *testing.T, cfg config.Configuration) {
	t.Parallel()
	deploymentID := loadTestYaml(t)
	infrastructure := commons.Infrastructure{}
	ctx := context.Background()
	outputs := make(map[string]string, 0)
	g := awsGenerator{}
	nodeParams := nodeParams{
		deploymentID:   deploymentID,
		nodeName:       "EBSVolume",
		infrastructure: &infrastructure,
	}

	err := g.generateEBS(ctx, nodeParams, "0", 0, outputs)
	if err != nil {
		panic(err)
	}

	require.NoError(t, err, "Unexpected error attempting to generate EBS for %s", deploymentID)
	require.Len(t, infrastructure.Resource["aws_ebs_volume"], 1, "Expected one ebs volume")
	instancesMap := infrastructure.Resource["aws_ebs_volume"].(map[string]interface{})
	require.Len(t, instancesMap, 1)

	diskName := "ebsvolume-0"
	require.Contains(t, instancesMap, diskName)

	ebsvolume, ok := instancesMap[diskName].(*EBSVolume)
	require.True(t, ok, "%s is not a EBSVolume", diskName)
	assert.Equal(t, "europe-west1-b", ebsvolume.AvailabilityZone)
	assert.Equal(t, 12, ebsvolume.Size)
	assert.Equal(t, true, ebsvolume.Encrypted)
	assert.Equal(t, "projects/project/global/snapshots/snapshot", ebsvolume.SnapshotID)
	assert.Equal(t, "arn:aws:kms:us-east-2:607034132673:key/8f947919-3432-4ace-ab11-d445a893d390", ebsvolume.KMSKeyID)
	assert.Equal(t, "500", ebsvolume.IOPS)
	assert.Equal(t, "standard", ebsvolume.Type)
	assert.NotNil(t, ebsvolume.Tags, "volume tags are not expected to be null")
	assert.Equal(t, 2, len(ebsvolume.Tags))
	assert.Equal(t, "foo", ebsvolume.Tags["tag1"])
	assert.Equal(t, "bar", ebsvolume.Tags["tag2"])
}

func testSimpleEBSWithVolumeID(t *testing.T, cfg config.Configuration) {
	t.Parallel()
	deploymentID := loadTestYaml(t)
	infrastructure := commons.Infrastructure{}
	ctx := context.Background()
	outputs := make(map[string]string, 0)
	g := awsGenerator{}
	nodeParams := nodeParams{
		deploymentID:   deploymentID,
		nodeName:       "EBSVolume",
		infrastructure: &infrastructure,
	}

	err := g.generateEBS(ctx, nodeParams, "0", 0, outputs)
	if err != nil {
		panic(err)
	}

	require.NoError(t, err, "Unexpected error attempting to generate EBS for %s", deploymentID)
	require.Nil(t, infrastructure.Resource["aws_ebs_volume"], "Expected no ebs volume")

	volumeIDKey := nodeParams.nodeName + "-0-id"
	require.NotNil(t, infrastructure.Output[volumeIDKey], "Expected related output to EBS volume ID")
	output := infrastructure.Output[volumeIDKey]
	require.Equal(t, output.Value, "vol-04313e68765dd91fb")
}
