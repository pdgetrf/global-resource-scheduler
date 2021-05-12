/*
Copyright 2020 Authors of Arktos.

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

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gopkg.in/yaml.v2"
	"io/ioutil"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/klog"
	allocclientset "k8s.io/kubernetes/globalscheduler/pkg/apis/allocation/client/clientset/versioned"
	v1 "k8s.io/kubernetes/globalscheduler/pkg/apis/allocation/v1"
	"net/http"
	"sync"
	"time"
)

type AllocationHandler struct {
	mu        sync.Mutex
	clientset *allocclientset.Clientset
}

func NewAllocationHandler() *AllocationHandler {
	config := getConfig()
	clientset, err := allocclientset.NewForConfig(config)
	if err != nil {
		klog.Fatalf("Failed to create a new allocation handler with the error %v", err)
		return nil
	}
	allocHandler := &AllocationHandler{
		clientset: clientset,
	}
	return allocHandler
}

func (handler *AllocationHandler) getAllocation(w http.ResponseWriter, r *http.Request) (string, error) {
	namespace, name := getNamespaceAndName(r)
	var allocstr []byte
	if name == "" {
		allocations, err := handler.clientset.GlobalschedulerV1().Allocations(namespace).List(metav1.ListOptions{})
		if err != nil {
			return "", nil
		}
		allocstr, err = yaml.Marshal(allocations)
	} else {
		allocation, err := handler.clientset.GlobalschedulerV1().Allocations(namespace).Get(name, metav1.GetOptions{})
		if err != nil {
			return "", err
		}
		allocstr, err = yaml.Marshal(allocation)
	}
	return string(allocstr), nil
}

func (handler *AllocationHandler) createAllocation(w http.ResponseWriter, r *http.Request) error {
	namespace, _ := getNamespaceAndName(r)
	reqBody, err := ioutil.ReadAll(r.Body)
	if err != nil {
		klog.Errorf("Failed to read allocations with the error %v", err)
		return err
	}
	alloc, err := yaml2Allocation(reqBody)
	if err != nil {
		klog.Errorf("Failed to covert yaml to allocation with the error %v", err)
		return err
	}
	createdAlloc, err := handler.clientset.GlobalschedulerV1().Allocations(namespace).Create(&alloc)
	if err != nil {
		klog.Errorf("Failed to create the allocation %v with the error %v", alloc, err)
		return err
	}
	duration := int64(TimeOut * time.Second * 2)
	options := metav1.ListOptions{
		TimeoutSeconds:  &duration,
		Watch:           true,
		ResourceVersion: createdAlloc.ResourceVersion,
		FieldSelector:   fmt.Sprintf("metadata.name=%s", createdAlloc.Name),
	}
	watcher := handler.clientset.GlobalschedulerV1().Allocations(namespace).Watch(options)
	timer := time.NewTimer(TimeOut * time.Second)
	return handler.watchAllocationPhase(namespace, createdAlloc.Name, createdAlloc, r.Context(), watcher, timer)
}

func (handler *AllocationHandler) putAllocation(w http.ResponseWriter, r *http.Request) error {
	namespace, _ := getNamespaceAndName(r)
	reqBody, err := ioutil.ReadAll(r.Body)
	if err != nil {
		klog.Errorf("Failed to read allocations with the error %v", err)
		return err
	}
	alloc, err := yaml2Allocation(reqBody)
	if err != nil {
		klog.Errorf("Failed to covert yaml to allocation with the error %v", err)
		return err
	}

	updatedAlloc, err := handler.clientset.GlobalschedulerV1().Allocations(namespace).Update(&alloc)
	if err != nil {
		klog.Errorf("Failed to update the allocation %v with the error %v", alloc, err)
		return err
	}
	duration := int64(TimeOut * time.Second)
	options := metav1.ListOptions{
		TimeoutSeconds:  &duration,
		Watch:           true,
		ResourceVersion: updatedAlloc.ResourceVersion,
		FieldSelector:   fmt.Sprintf("metadata.name=%s", updatedAlloc.Name),
	}
	watcher := handler.clientset.GlobalschedulerV1().Allocations(namespace).Watch(options)
	timer := time.NewTimer(TimeOut * time.Second)
	return handler.watchAllocationPhase(namespace, updatedAlloc.Name, updatedAlloc, r.Context(), watcher, timer)
}

func (handler *AllocationHandler) patchAllocation(w http.ResponseWriter, r *http.Request) error {
	namespace, name := getNamespaceAndName(r)
	reqBody, err := ioutil.ReadAll(r.Body)
	if err != nil {
		klog.Errorf("Failed to read allocations with the error %v", err)
		return err
	}
	jsonstr, err := yaml2json(reqBody)
	if err != nil {
		klog.Errorf("Failed to convert yaml %s to allocation with the error %v", string(reqBody), err)
		return err
	}
	patchedAlloc, err := handler.clientset.GlobalschedulerV1().Allocations(namespace).Patch(name, types.MergePatchType, []byte(jsonstr))
	if err != nil {
		klog.Errorf("There is an error [%v] in patch application", err)
		return err
	}

	duration := int64(TimeOut * time.Second)
	options := metav1.ListOptions{
		TimeoutSeconds:  &duration,
		Watch:           true,
		ResourceVersion: patchedAlloc.ResourceVersion,
		FieldSelector:   fmt.Sprintf("metadata.name=%s", name),
	}
	watcher := handler.clientset.GlobalschedulerV1().Allocations(namespace).Watch(options)
	timer := time.NewTimer(TimeOut * time.Second)
	return handler.watchAllocationPhase(namespace, name, patchedAlloc, r.Context(), watcher, timer)
}

func (handler *AllocationHandler) deleteAllocation(w http.ResponseWriter, r *http.Request) error {
	namespace, name := getNamespaceAndName(r)
	return handler.clientset.GlobalschedulerV1().Allocations(namespace).Delete(name, &metav1.DeleteOptions{})
}

func (handler *AllocationHandler) watchAllocationPhase(namespace, name string, alloc *v1.Allocation, ctx context.Context, watcher watch.AggregatedWatchInterface, timer *time.Timer) error {
	defer watcher.Stop()
	status := string(alloc.Status.Phase)
	if status == string(corev1.ClusterScheduled) {
		return nil
	}
	for {
		select {
		case event := <-watcher.ResultChan():
			allocObj, ok := event.Object.(*v1.Allocation)
			if ok {
				status = string(allocObj.Status.Phase)
				if status == string(corev1.ClusterScheduled) {
					return nil
				}
			}
		case <-timer.C:
			if status != string(corev1.ClusterScheduled) {
				return errors.New(fmt.Sprintf("The allocation status %s is not scheduled after timeout", status))
			} else {
				return nil
			}
		case <-ctx.Done():
			err := ctx.Err()
			if err != nil {
				klog.Errorf("There is a server error %v", err)
			}
			if status != string(corev1.ClusterScheduled) {
				return errors.New(fmt.Sprintf("The allocation status %s is not expected when the context is done.", status))
			} else {
				return nil
			}
		}
	}
	return nil
}

func (handler *AllocationHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if r.URL.Path != "/allocations" {
		http.NotFound(w, r)
		return
	}
	result := ""
	var err error
	switch r.Method {
	case "GET":
		result, err = handler.getAllocation(w, r)
	case "POST":
		err = handler.createAllocation(w, r)
		result = http.StatusText(http.StatusCreated)
	case "PUT":
		err = handler.putAllocation(w, r)
		result = http.StatusText(http.StatusAccepted)
	case "PATCH":
		err = handler.patchAllocation(w, r)
		result = http.StatusText(http.StatusAccepted)
	case "DELETE":
		err = handler.deleteAllocation(w, r)
		result = http.StatusText(http.StatusAccepted)
	default:
		result = http.StatusText(http.StatusNotImplemented)
		w.WriteHeader(http.StatusNotImplemented)
	}
	if err != nil {
		internalError := http.StatusInternalServerError
		http.Error(w, err.Error(), internalError)
	} else if result != "" {
		w.Write([]byte(result))
	}
}

func yaml2Allocation(reqBody []byte) (alloc v1.Allocation, err error) {
	if str, err := yaml2json(reqBody); err != nil {
		klog.Errorf("Failed to convert to json with the error: %v", err)
	} else {
		err = json.Unmarshal([]byte(str), &alloc)
	}
	return alloc, err
}

func getNamespaceAndName(r *http.Request) (string, string) {
	var name, namespace string
	namespace = "default"
	names, ok := r.URL.Query()["name"]
	if ok && len(names[0]) > 0 {
		name = names[0]
	}
	namespaces, ok := r.URL.Query()["namespace"]
	if ok && len(namespaces[0]) > 0 {
		namespace = namespaces[0]
	}
	return namespace, name
}