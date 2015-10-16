package workers

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/convox/rack/api/helpers"
	"github.com/convox/rack/api/models"

	"github.com/convox/rack/api/Godeps/_workspace/src/github.com/aws/aws-sdk-go/aws"
	"github.com/convox/rack/api/Godeps/_workspace/src/github.com/aws/aws-sdk-go/service/autoscaling"
	"github.com/convox/rack/api/Godeps/_workspace/src/github.com/aws/aws-sdk-go/service/cloudformation"
	"github.com/convox/rack/api/Godeps/_workspace/src/github.com/aws/aws-sdk-go/service/ecs"
	"github.com/convox/rack/api/Godeps/_workspace/src/github.com/ddollar/logger"
)

type Instance struct {
	Id     string
	ECS    bool
	ASG    bool
	Docker bool
	Run    bool
}

type Instances map[string]Instance

func StartCluster() {
	var log = logger.New("ns=cluster_monitor")

	defer recoverWith(func(err error) {
		helpers.Error(log, err)
	})

	for _ = range time.Tick(30 * time.Second) {
		log.Log("tick")

		instanceCount, err := getRackInstanceCount()

		if err != nil {
			log.Error(err)
			continue
		}

		instances := Instances{}

		err = instances.describeASG()

		if err != nil {
			log.Error(err)
			continue
		}

		err = instances.describeECS()

		if err != nil {
			log.Error(err)
			continue
		}

		// Test if ASG Instance is registered and connected in ECS cluster

		aInstanceIds := []string{}
		cInstanceIds := []string{}
		uInstanceIds := []string{}

		for _, i := range instances {
			if i.ECS {
				aInstanceIds = append(aInstanceIds, i.Id)
				cInstanceIds = append(cInstanceIds, i.Id)
			} else {
				// Not registered or not connected => set Unhealthy
				if i.ASG {
					_, err := models.AutoScaling().SetInstanceHealth(
						&autoscaling.SetInstanceHealthInput{
							HealthStatus:             aws.String("Unhealthy"),
							InstanceId:               aws.String(i.Id),
							ShouldRespectGracePeriod: aws.Bool(true),
						},
					)

					if err != nil {
						log.Error(err)
						continue
					}

					uInstanceIds = append(uInstanceIds, i.Id)
				}
			}
		}

		sort.Strings(aInstanceIds)
		sort.Strings(cInstanceIds)
		sort.Strings(uInstanceIds)

		log.Log("InstanceCount=%v connected='%v' healthy='%v' marked='%s'", instanceCount, strings.Join(cInstanceIds, ","), strings.Join(aInstanceIds, ","), strings.Join(uInstanceIds, ","))
	}
}

func (instances Instances) describeASG() error {
	resources, err := models.ListResources(os.Getenv("RACK"))

	res, err := models.AutoScaling().DescribeAutoScalingGroups(
		&autoscaling.DescribeAutoScalingGroupsInput{
			AutoScalingGroupNames: []*string{
				aws.String(resources["Instances"].Id),
			},
		},
	)

	if err != nil {
		return err
	}

	for _, i := range res.AutoScalingGroups[0].Instances {
		instance := instances[*i.InstanceId]

		instance.Id = *i.InstanceId
		instance.ASG = *i.LifecycleState == "InService"

		instances[*i.InstanceId] = instance
	}

	return nil
}

func (instances Instances) describeECS() error {
	res, err := models.ECS().ListContainerInstances(
		&ecs.ListContainerInstancesInput{
			Cluster: aws.String(os.Getenv("CLUSTER")),
		},
	)

	if err != nil {
		return err
	}

	dres, err := models.ECS().DescribeContainerInstances(
		&ecs.DescribeContainerInstancesInput{
			Cluster:            aws.String(os.Getenv("CLUSTER")),
			ContainerInstances: res.ContainerInstanceArns,
		},
	)

	if err != nil {
		return err
	}

	for _, i := range dres.ContainerInstances {
		instance := instances[*i.Ec2InstanceId]

		instance.Id = *i.Ec2InstanceId
		instance.ECS = *i.AgentConnected

		instances[*i.Ec2InstanceId] = instance
	}

	return nil
}

func getRackInstanceCount() (int, error) {
	name := os.Getenv("RACK")

	res, err := models.CloudFormation().DescribeStacks(
		&cloudformation.DescribeStacksInput{
			StackName: aws.String(name),
		},
	)

	if err != nil {
		return 0, err
	}

	for _, p := range res.Stacks[0].Parameters {
		if *p.ParameterKey == "InstanceCount" {
			c, err := strconv.Atoi(*p.ParameterValue)

			if err != nil {
				return 0, err
			}

			return c, nil
		}
	}

	return 0, fmt.Errorf("Stack %s InstanceCount parameter missing", name)
}
