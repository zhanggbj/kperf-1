// Copyright © 2020 The Knative Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package service

import (
	"context"
	"fmt"
	"github.com/pkg/errors"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	_ "k8s.io/client-go/plugin/pkg/client/auth/oidc"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	knativeapis "knative.dev/pkg/apis"
	servingv1 "knative.dev/serving/pkg/apis/serving/v1"
	servingv1client "knative.dev/serving/pkg/client/clientset/versioned/typed/serving/v1"

	"github.com/spf13/cobra"

	"github.com/knative.dev/kperf/pkg"
	"github.com/knative.dev/kperf/pkg/generator"
)

var (
	count, interval, batch, concurrency, minScale, maxScale int
	nsPrefix, nsRange, ns                                   string
	svcNamePrefixDefault                                    string = "testksvc"
	checkReady                                              bool
	timeout                                                 time.Duration
	ksvcClient                                              *servingv1client.ServingV1Client
	err                                                     error
)

func NewServiceGenerateCommand(p *pkg.PerfParams) *cobra.Command {
	ksvcGenCommand := &cobra.Command{
		Use:   "generate",
		Short: "generate ksvc",
		Long: `generate ksvc workload

For example:
# To generate ksvc workload
kperf service generate —n 500 —interval 20 —batch 20 --min-scale 0 --max-scale 5 (--nsprefix testns/ --ns nsname)
`,
		PreRunE: func(cmd *cobra.Command, args []string) error {
			flags := cmd.Flags()
			if flags.Changed("nsprefix") && flags.Changed("ns") {
				return errors.New("Expected either namespace with prefix & range or only namespace name")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			// Get namespace list from flags
			nsNameList := []string{}
			if nsPrefix == "" && ns == "" {
				nsNameList = []string{"default"}
			}
			if nsPrefix != "" {
				r := strings.Split(nsRange, ",")
				if len(r) != 2 {
					return errors.Errorf("Expected Range like 1,500, given %s\n", nsRange)
				}
				start, err := strconv.Atoi(r[0])
				if err != nil {
					return err
				}
				end, err := strconv.Atoi(r[1])
				if err != nil {
					return err
				}
				if start > 0 && end > 0 && start <= end {
					for i := start; i <= end; i++ {
						nsNameList = append(nsNameList, fmt.Sprintf("%s-%d", nsPrefix, i))
					}
				} else {
					return errors.New("failed to parse namespace range")
				}
			} else if ns != "" {
				nsNameList = append(nsNameList, ns)
			}

			if len(nsNameList) == 0 {
				return fmt.Errorf("no namespace found %s", nsPrefix)
			}

			// Check if namespace exists, in NOT, return error
			for _, ns := range nsNameList {
				_, err := p.ClientSet.CoreV1().Namespaces().Get(context.TODO(), ns, metav1.GetOptions{})
				if err != nil && apierrors.IsNotFound(err) {
					return errors.Errorf("namespace %s not found, please create one", ns)
				} else if err != nil {
					return errors.Wrap(err, "failed to get namespace")
				}
			}

			restConfig, err := p.RestConfig()
			ksvcClient, err = servingv1client.NewForConfig(restConfig)
			if err != nil {
				return err
			}

			if checkReady {
				generator.NewBatchGenerator(time.Duration(interval)*time.Second, count, batch, concurrency, nsNameList, createKSVC, checkServiceStatusReady, p).Generate()
			} else {
				generator.NewBatchGenerator(time.Duration(interval)*time.Second, count, batch, concurrency, nsNameList, createKSVC, func(ns, name string) error { return nil }, p).Generate()
			}

			return nil
		},
	}
	// count, interval, batch, minScale, maxScale int
	ksvcGenCommand.Flags().IntVarP(&count, "number", "n", 0, "Total number of ksvc to be created")
	ksvcGenCommand.MarkFlagRequired("count")
	ksvcGenCommand.Flags().IntVarP(&interval, "interval", "i", 0, "Interval for each batch generation")
	ksvcGenCommand.MarkFlagRequired("interval")
	ksvcGenCommand.Flags().IntVarP(&batch, "batch", "b", 0, "Number of ksvc each time to be created")
	ksvcGenCommand.MarkFlagRequired("batch")
	ksvcGenCommand.Flags().IntVarP(&concurrency, "concurrency", "c", 10, "Number of multiple ksvcs to make at a time")
	// ksvcGenCommand.MarkFlagRequired("concurrency")
	ksvcGenCommand.Flags().IntVarP(&minScale, "minScale", "", 0, "For autoscaling.knative.dev/minScale")
	ksvcGenCommand.MarkFlagRequired("minScale")
	ksvcGenCommand.Flags().IntVarP(&maxScale, "maxScale", "", 0, "For autoscaling.knative.dev/minScale")
	ksvcGenCommand.MarkFlagRequired("minScale")

	ksvcGenCommand.Flags().StringVarP(&nsPrefix, "nsPrefix", "p", "", "Namespace prefix. The ksvc will be created in the namespaces with the prefix")
	ksvcGenCommand.Flags().StringVarP(&nsRange, "nsRange", "", "", "")
	ksvcGenCommand.Flags().StringVarP(&ns, "ns", "", "", "Namespace name. The ksvc will be created in the namespace")

	ksvcGenCommand.Flags().StringVarP(&svcPrefix, "svcPrefix", "", "testksvc", "ksvc name prefix. The ksvcs will be svcPrefix1,svcPrefix2,svcPrefix3......")
	ksvcGenCommand.Flags().BoolVarP(&checkReady, "wait", "", false, "whether wait the previous ksvc to be ready")
	ksvcGenCommand.Flags().DurationVarP(&timeout, "timeout", "", 10*time.Minute, "duration to wait for previous ksvc to be ready")

	return ksvcGenCommand
}

func createKSVC(p *pkg.PerfParams, ns string, index int) (string, string) {
	service := servingv1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%d", svcPrefix, index),
			Namespace: ns,
		},
	}

	service.Spec.Template = servingv1.RevisionTemplateSpec{
		Spec: servingv1.RevisionSpec{},
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				"autoscaling.knative.dev/minScale": strconv.Itoa(minScale),
				"autoscaling.knative.dev/maxScale": strconv.Itoa(maxScale),
			},
		},
	}
	service.Spec.Template.Spec.Containers = []corev1.Container{
		{
			Image: "gcr.io/knative-samples/helloworld-go",
			Ports: []corev1.ContainerPort{
				{
					ContainerPort: 8080,
				},
			},
		},
	}
	fmt.Printf("Creating ksvc %s in namespace %s\n", service.GetName(), service.GetNamespace())
	ksvcClient, err := p.NewServingClient()
	if err != nil {
		fmt.Printf("Failed to create serving client: %s\n", err)
	}
	_, err = ksvcClient.Services(ns).Create(context.TODO(), &service, metav1.CreateOptions{})
	if err != nil {
		fmt.Printf("Failed to create ksvc %s in namespace %s : %s\n", service.GetName(), service.GetNamespace(), err)
	}
	return service.GetNamespace(), service.GetName()
}

func checkServiceStatusReady(ns, name string) error {
	start := time.Now()
	for time.Now().Sub(start) < timeout {
		svc, _ := ksvcClient.Services(ns).Get(context.TODO(), name, metav1.GetOptions{})
		conditions := svc.Status.Conditions
		for i := 0; i < len(conditions); i++ {
			if conditions[i].Type == knativeapis.ConditionReady && conditions[i].IsTrue() {
				return nil
			}
		}
	}
	fmt.Printf("Error: ksvc %s in namespace %s is not ready after %s\n", name, ns, timeout)
	return fmt.Errorf("ksvc %s in namespace %s is not ready after %s ", name, ns, timeout)
}
