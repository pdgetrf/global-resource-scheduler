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

package rpcserver

import (
	"context"
	"errors"
	"fmt"
	"k8s.io/klog"
	pb "k8s.io/kubernetes/globalscheduler/grpc/cluster/proto"
	"k8s.io/kubernetes/globalscheduler/pkg/scheduler/client/typed"
	"k8s.io/kubernetes/resourcecollector/pkg/collector"
	"k8s.io/kubernetes/resourcecollector/pkg/collector/task"
)

const (
	ClusterStatusCreated = "Created"
	ClusterStatusDeleted = "Deleted"
)

type ClusterProtocolServer struct{}

// services - Send cluster profile
func (s *ClusterProtocolServer) SendClusterProfile(ctx context.Context,
	in *pb.ClusterProfile) (*pb.ReturnMessageClusterProfile, error) {
	klog.Infof("Received RPC Request")
	ns := in.ClusterNameSpace
	name := in.ClusterName
	if in.ClusterSpec == nil || in.ClusterSpec.GeoLocation == nil || in.ClusterSpec.Region == nil {
		err := errors.New("clusterSpec information is incomplete")
		klog.Info("clusterSpec information is incomplete")
		return getReturnMessageFromError(ns, name, &err), err
	}
	ip := in.ClusterSpec.ClusterIpAddress
	if ip == "" {
		err := errors.New("cluster ip is not set")
		klog.Info("cluster ip is not set")
		return getReturnMessageFromError(ns, name, &err), err
	}
	region := in.ClusterSpec.Region.Region
	az := in.ClusterSpec.Region.AvailabilityZone
	if region == "" || az == "" {
		err := errors.New("cluster region or az is invalid")
		klog.Info("cluster region or az is invalid")
		return getReturnMessageFromError(ns, name, &err), err
	}
	siteID := fmt.Sprintf("%s|%s", region, az)

	col, err := collector.GetCollector()
	if err != nil {
		klog.Errorf("get new collector failed, err: %s", err.Error())
		err := errors.New("server internal error")
		return getReturnMessageFromError(ns, name, &err), err
	}
	switch in.ClusterStatus {
	case ClusterStatusCreated:
		klog.Infof("grpc.GrpcSendClusterProfile created - region[%s], IP[%s]", region, ip)
		regionInfo := &typed.RegionResource{
			RegionName:   region,
			EipNetworkID: ip,
		}
		col.GetRegionResources().AddRegionResource(regionInfo)
		col.GetRegionResources().AddAvailabilityZone(region, az)
		task.BuildHostAzMap(col)
	case ClusterStatusDeleted:
		klog.Infof("grpc.GrpcSendClusterProfile deleted - region[%s], IP[%s]", region, ip)
		err := col.GetRegionResources().RemoveAvailabilityZone(region, az)
		if err != nil {
			klog.Errorf("RemoveAvailabilityZone err: %s", err.Error())
		}
		col.GetRegionResources().RemoveRegionResource(region)
		if err != nil {
			klog.Errorf("RemoveRegionResource err: %s", err.Error())
		}

	default:
		klog.Infof("grpc.GrpcSendClusterProfile status error[%s]- siteID[%s], IP[%s]", in.ClusterStatus, siteID, ip)
		err := errors.New("status error")
		return getReturnMessageFromError(ns, name, &err), err
	}

	resp := &pb.ReturnMessageClusterProfile{
		ClusterNameSpace: ns,
		ClusterName:      name,
		ReturnCode:       pb.ReturnMessageClusterProfile_OK,
		Message:          "ok",
	}
	return resp, nil
}

func getReturnMessageFromError(ns, name string, err *error) *pb.ReturnMessageClusterProfile {
	return &pb.ReturnMessageClusterProfile{
		ClusterNameSpace: ns,
		ClusterName:      name,
		ReturnCode:       pb.ReturnMessageClusterProfile_Error,
		Message:          fmt.Sprintf("Grpc call failed: %s", (*err).Error()),
	}
}
