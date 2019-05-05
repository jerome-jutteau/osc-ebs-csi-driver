/*
Copyright 2018 The Kubernetes Authors.

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

package e2e

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"strings"

	awscloud "github.com/kubernetes-sigs/aws-ebs-csi-driver/pkg/cloud"
	"github.com/kubernetes-sigs/aws-ebs-csi-driver/tests/e2e/driver"
	"github.com/kubernetes-sigs/aws-ebs-csi-driver/tests/e2e/testsuites"
	. "github.com/onsi/ginkgo"
	v1 "k8s.io/api/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"

	ebscsidriver "github.com/kubernetes-sigs/aws-ebs-csi-driver/pkg/driver"
)

const (
	defaultDiskSize   = 4
	defaultVoluemType = awscloud.VolumeTypeGP2

	awsAvailabilityZonesEnv = "AWS_AVAILABILITY_ZONES"

	dummyVolumeName = "pre-provisioned"
)

var (
	defaultDiskSizeBytes int64 = defaultDiskSize * 1024 * 1024 * 1024
)

type e2eMetdataService struct {
	availabilityZone string
}

// GetInstanceID will always return an empty string as the test does not need to run on an EC2 machine
func (s e2eMetdataService) GetInstanceID() string {
	return ""
}

func (s e2eMetdataService) GetInstanceType() string {
	return ""
}

func (s e2eMetdataService) GetAvailabilityZone() string {
	return s.availabilityZone
}

// GetRegion will try to determine the Region from the specified AZ, specifically trims the last character
func (s e2eMetdataService) GetRegion() string {
	return s.availabilityZone[0 : len(s.availabilityZone)-1]
}

// Requires env AWS_AVAILABILITY_ZONES a comma separated list of AZs to be set
var _ = Describe("[ebs-csi-e2e] [single-az] Pre-Provisioned", func() {
	f := framework.NewDefaultFramework("ebs")

	var (
		cs        clientset.Interface
		ns        *v1.Namespace
		ebsDriver driver.PreProvisionedVolumeTestDriver
		cloud     awscloud.Cloud
		volumeID  string
		diskSize  string
		// Set to true if the volume should be deleted automatically after test
		skipManuallyDeletingVolume bool
	)

	BeforeEach(func() {
		cs = f.ClientSet
		ns = f.Namespace
		ebsDriver = driver.InitEbsCSIDriver()

		// setup EBS volume
		if os.Getenv(awsAvailabilityZonesEnv) == "" {
			Skip(fmt.Sprintf("env %q not set", awsAvailabilityZonesEnv))
		}
		availabilityZones := strings.Split(os.Getenv(awsAvailabilityZonesEnv), ",")
		availabilityZone := availabilityZones[rand.Intn(len(availabilityZones))]
		diskOptions := &awscloud.DiskOptions{
			CapacityBytes:    defaultDiskSizeBytes,
			VolumeType:       defaultVoluemType,
			AvailabilityZone: availabilityZone,
			Tags:             map[string]string{awscloud.VolumeNameTagKey: dummyVolumeName},
		}
		metadata := e2eMetdataService{availabilityZone: availabilityZone}
		var err error
		cloud, err = awscloud.NewCloudWithMetadata(metadata)
		if err != nil {
			Fail(fmt.Sprintf("could not get NewCloud: %v", err))
		}
		disk, err := cloud.CreateDisk(context.Background(), "", diskOptions)
		if err != nil {
			Fail(fmt.Sprintf("could not provision a volume: %v", err))
		}
		volumeID = disk.VolumeID
		diskSize = fmt.Sprintf("%dGi", defaultDiskSize)
		By(fmt.Sprintf("Successfully provisioned EBS volume: %q\n", volumeID))
	})

	AfterEach(func() {
		if !skipManuallyDeletingVolume {
			err := cloud.WaitForAttachmentState(context.Background(), volumeID, "detached")
			if err != nil {
				Fail(fmt.Sprintf("could not detach volume %q: %v", volumeID, err))
			}
			ok, err := cloud.DeleteDisk(context.Background(), volumeID)
			if err != nil || !ok {
				Fail(fmt.Sprintf("could not delete volume %q: %v", volumeID, err))
			}
		}
	})

	It("[env] should write and read to a pre-provisioned volume", func() {
		pods := []testsuites.PodDetails{
			{
				Cmd: "echo 'hello world' > /mnt/test-1/data && grep 'hello world' /mnt/test-1/data",
				Volumes: []testsuites.VolumeDetails{
					{
						VolumeID:  volumeID,
						FSType:    ebscsidriver.FSTypeExt4,
						ClaimSize: diskSize,
						VolumeMount: testsuites.VolumeMountDetails{
							NameGenerate:      "test-volume-",
							MountPathGenerate: "/mnt/test-",
						},
					},
				},
			},
		}
		test := testsuites.PreProvisionedVolumeTest{
			CSIDriver: ebsDriver,
			Pods:      pods,
		}
		test.Run(cs, ns)
	})

	It("[env] should use a pre-provisioned volume and mount it as readOnly in a pod", func() {
		pods := []testsuites.PodDetails{
			{
				Cmd: "echo 'hello world' > /mnt/test-1/data && grep 'hello world' /mnt/test-1/data",
				Volumes: []testsuites.VolumeDetails{
					{
						VolumeID:  volumeID,
						FSType:    ebscsidriver.FSTypeExt4,
						ClaimSize: diskSize,
						VolumeMount: testsuites.VolumeMountDetails{
							NameGenerate:      "test-volume-",
							MountPathGenerate: "/mnt/test-",
							ReadOnly:          true,
						},
					},
				},
			},
		}
		test := testsuites.PreProvisionedReadOnlyVolumeTest{
			CSIDriver: ebsDriver,
			Pods:      pods,
		}
		test.Run(cs, ns)
	})

	It(fmt.Sprintf("[env] should use a pre-provisioned volume and retain PV with reclaimPolicy %q", v1.PersistentVolumeReclaimRetain), func() {
		reclaimPolicy := v1.PersistentVolumeReclaimRetain
		volumes := []testsuites.VolumeDetails{
			{
				VolumeID:      volumeID,
				FSType:        ebscsidriver.FSTypeExt4,
				ClaimSize:     diskSize,
				ReclaimPolicy: &reclaimPolicy,
			},
		}
		test := testsuites.PreProvisionedReclaimPolicyTest{
			CSIDriver: ebsDriver,
			Volumes:   volumes,
		}
		test.Run(cs, ns)
	})

	It(fmt.Sprintf("[env] should use a pre-provisioned volume and delete PV with reclaimPolicy %q", v1.PersistentVolumeReclaimDelete), func() {
		reclaimPolicy := v1.PersistentVolumeReclaimDelete
		skipManuallyDeletingVolume = true
		volumes := []testsuites.VolumeDetails{
			{
				VolumeID:      volumeID,
				FSType:        ebscsidriver.FSTypeExt4,
				ClaimSize:     diskSize,
				ReclaimPolicy: &reclaimPolicy,
			},
		}
		test := testsuites.PreProvisionedReclaimPolicyTest{
			CSIDriver: ebsDriver,
			Volumes:   volumes,
		}
		test.Run(cs, ns)
	})
})
