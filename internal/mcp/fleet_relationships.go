package mcp

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/skyhook-io/radar/internal/fleet"
	"github.com/skyhook-io/radar/pkg/resourceid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var (
	karmadaClustersGVR                = schema.GroupVersionResource{Group: "cluster.karmada.io", Version: "v1alpha1", Resource: "clusters"}
	karmadaResourceBindingsGVR        = schema.GroupVersionResource{Group: "work.karmada.io", Version: "v1alpha2", Resource: "resourcebindings"}
	karmadaClusterResourceBindingsGVR = schema.GroupVersionResource{Group: "work.karmada.io", Version: "v1alpha2", Resource: "clusterresourcebindings"}
)

type fleetRelationship struct {
	ID               string          `json:"id"`
	Type             string          `json:"type"`
	SourceClusterID  string          `json:"sourceClusterId"`
	TargetClusterID  string          `json:"targetClusterId,omitempty"`
	TargetCandidates []string        `json:"targetCandidates,omitempty"`
	State            string          `json:"state"`
	MemberName       string          `json:"memberName,omitempty"`
	SourceResource   *fleetResource  `json:"sourceResource,omitempty"`
	TargetResource   *fleetResource  `json:"targetResource,omitempty"`
	Evidence         []fleetResource `json:"evidence"`
}

type fleetRelationshipCoverage struct {
	ClusterID string `json:"clusterId"`
	Resource  string `json:"resource"`
	State     string `json:"state"`
}

type fleetRelationshipsResult struct {
	Relationships []fleetRelationship         `json:"relationships"`
	Coverage      []fleetRelationshipCoverage `json:"coverage"`
	Truncated     bool                        `json:"truncated"`
	Limitations   []string                    `json:"limitations"`
}

type fleetTopologyNode struct {
	ID        string          `json:"id"`
	ClusterID string          `json:"clusterId,omitempty"`
	Label     string          `json:"label"`
	Type      string          `json:"type"`
	Ref       *resourceid.Ref `json:"ref,omitempty"`
	UID       string          `json:"uid,omitempty"`
}

type fleetTopologyEdge struct {
	ID       string          `json:"id"`
	Source   string          `json:"source"`
	Target   string          `json:"target"`
	Type     string          `json:"type"`
	State    string          `json:"state"`
	Evidence []fleetResource `json:"evidence"`
}

type fleetTopologyResult struct {
	Nodes       []fleetTopologyNode         `json:"nodes"`
	Edges       []fleetTopologyEdge         `json:"edges"`
	Coverage    []fleetRelationshipCoverage `json:"coverage"`
	Truncated   bool                        `json:"truncated"`
	Limitations []string                    `json:"limitations"`
}

func (t fleetTools) getFleetRelationships(ctx context.Context, _ *mcpsdk.CallToolRequest, input fleetSelection) (*mcpsdk.CallToolResult, fleetRelationshipsResult, error) {
	selected, err := t.selectClusters(ctx, input)
	if err != nil {
		return nil, fleetRelationshipsResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	result := fleetRelationshipsResult{Relationships: []fleetRelationship{}, Coverage: []fleetRelationshipCoverage{}, Limitations: []string{}}
	freshUIDs := map[string][]string{}
	memberTargets := map[string][]string{}
	clients := map[string]dynamic.Interface{}
	namespacesByCluster := map[string][]string{}
	statusesByID := map[string]fleet.ConnectionStatus{}
	for _, status := range selected {
		statusesByID[status.ClusterID] = status
		typed, dynamic, namespaces, err := t.clients(ctx, status.ClusterID)
		if err != nil {
			for _, resource := range []string{"identity", "clusters", "resourcebindings", "clusterresourcebindings"} {
				result.Coverage = append(result.Coverage, fleetRelationshipCoverage{status.ClusterID, resource, "unavailable"})
			}
			continue
		}
		clients[status.ClusterID], namespacesByCluster[status.ClusterID] = dynamic, namespaces
		if !fleetNamespaceAllowed(namespaces, "kube-system") {
			result.Coverage = append(result.Coverage, fleetRelationshipCoverage{status.ClusterID, "identity", "out_of_scope"})
			continue
		}
		ns, err := typed.CoreV1().Namespaces().Get(ctx, "kube-system", metav1.GetOptions{})
		if err == nil {
			if ns.UID == "" {
				result.Coverage = append(result.Coverage, fleetRelationshipCoverage{status.ClusterID, "identity", "unresolved"})
			} else {
				freshUIDs[string(ns.UID)] = append(freshUIDs[string(ns.UID)], status.ClusterID)
				result.Coverage = append(result.Coverage, fleetRelationshipCoverage{status.ClusterID, "identity", "complete"})
			}
		} else {
			result.Coverage = append(result.Coverage, fleetRelationshipCoverage{status.ClusterID, "identity", classifyFleetReadError(err)})
		}
	}
	for _, status := range selected {
		dynamic := clients[status.ClusterID]
		if dynamic == nil {
			continue
		}
		list, err := dynamic.Resource(karmadaClustersGVR).List(ctx, metav1.ListOptions{Limit: 200})
		if err != nil {
			state := classifyFleetReadError(err)
			if state == "not_found" {
				state = "unsupported"
			}
			result.Coverage = append(result.Coverage, fleetRelationshipCoverage{status.ClusterID, "clusters", state})
			continue
		}
		if list.GetContinue() != "" {
			result.Truncated = true
		}
		result.Coverage = append(result.Coverage, fleetRelationshipCoverage{status.ClusterID, "clusters", "complete"})
		for _, member := range list.Items {
			memberID, _, _ := unstructured.NestedString(member.Object, "spec", "id")
			targets := fleetIdentityCandidates(memberID, freshUIDs)
			memberTargets[status.ClusterID+":"+member.GetName()] = append([]string{}, targets...)
			evidence := fleetTargetFromObject(status, evidenceKind{GVR: karmadaClustersGVR, Kind: "Cluster"}, &member)
			rel := fleetRelationship{ID: "identity:" + status.ClusterID + ":" + member.GetName(), Type: "declared_identity_match", SourceClusterID: status.ClusterID, MemberName: member.GetName(), State: "unresolved", Evidence: []fleetResource{evidence}}
			if len(targets) == 1 {
				rel.TargetClusterID, rel.State = targets[0], "declared_identity_match"
			} else if len(targets) > 1 {
				rel.TargetCandidates, rel.State = append([]string{}, targets...), "ambiguous"
			}
			result.Relationships = append(result.Relationships, rel)
			if len(result.Relationships) >= 200 {
				result.Truncated = true
				return nil, result, nil
			}
		}
		for _, gvr := range []struct {
			gvr  schema.GroupVersionResource
			kind string
		}{{karmadaResourceBindingsGVR, "ResourceBinding"}, {karmadaClusterResourceBindingsGVR, "ClusterResourceBinding"}} {
			bindingNamespaces := []string{""}
			if gvr.gvr.Resource == karmadaResourceBindingsGVR.Resource && len(namespacesByCluster[status.ClusterID]) > 0 {
				bindingNamespaces = append([]string{}, namespacesByCluster[status.ClusterID]...)
			}
			coverageState := "complete"
			for _, namespace := range bindingNamespaces {
				bindings, bindErr := dynamic.Resource(gvr.gvr).Namespace(namespace).List(ctx, metav1.ListOptions{Limit: 200})
				if bindErr != nil {
					coverageState = classifyFleetReadError(bindErr)
					if coverageState == "not_found" {
						coverageState = "unsupported"
					}
					continue
				}
				if bindings.GetContinue() != "" {
					result.Truncated = true
				}
				for _, binding := range bindings.Items {
					members, _, _ := unstructured.NestedSlice(binding.Object, "spec", "clusters")
					for _, rawMember := range members {
						member, ok := rawMember.(map[string]any)
						if !ok {
							continue
						}
						memberName, _ := member["name"].(string)
						rel := t.bindingRelationship(ctx, status, gvr.kind, &binding, memberName, clients, namespacesByCluster, memberTargets, statusesByID)
						if rel != nil {
							result.Relationships = append(result.Relationships, *rel)
							if len(result.Relationships) >= 200 {
								result.Truncated = true
								return nil, result, nil
							}
						}
					}
				}
			}
			result.Coverage = append(result.Coverage, fleetRelationshipCoverage{status.ClusterID, gvr.gvr.Resource, coverageState})
		}
	}
	return nil, result, nil
}

func fleetIdentityCandidates(specID string, freshUIDs map[string][]string) []string {
	if specID == "" {
		return []string{}
	}
	candidates := append([]string{}, freshUIDs[specID]...)
	sort.Strings(candidates)
	return candidates
}

func (t fleetTools) bindingRelationship(ctx context.Context, source fleet.ConnectionStatus, kind string, binding *unstructured.Unstructured, memberName string, clients map[string]dynamic.Interface, namespaces map[string][]string, identities map[string][]string, statuses map[string]fleet.ConnectionStatus) *fleetRelationship {
	raw, ok, _ := unstructured.NestedMap(binding.Object, "spec", "resource")
	if !ok {
		return nil
	}
	apiVersion, _ := raw["apiVersion"].(string)
	refKind, _ := raw["kind"].(string)
	name, _ := raw["name"].(string)
	namespace, _ := raw["namespace"].(string)
	recordedUID, _ := raw["uid"].(string)
	allowed, ok := evidenceKindFor(strings.ToLower(refKind) + "s")
	gv, gvErr := schema.ParseGroupVersion(apiVersion)
	if !ok || gvErr != nil || gv != allowed.GVR.GroupVersion() || refKind != allowed.Kind || name == "" {
		return nil
	}
	if allowed.Namespaced != (namespace != "") {
		return nil
	}
	candidates := append([]string{}, identities[source.ClusterID+":"+memberName]...)
	sort.Strings(candidates)
	bindingID := string(binding.GetUID())
	if bindingID == "" {
		bindingID = binding.GetName() + ":" + binding.GetResourceVersion()
	}
	rel := &fleetRelationship{ID: "distribution:" + source.ClusterID + ":" + bindingID + ":" + memberName, Type: "declared_distribution", SourceClusterID: source.ClusterID, MemberName: memberName, State: "unresolved", Evidence: []fleetResource{fleetTargetFromObject(source, evidenceKind{GVR: schema.GroupVersionResource{Group: "work.karmada.io", Version: "v1alpha2", Resource: strings.ToLower(kind) + "s"}, Kind: kind}, binding)}}
	if len(candidates) == 1 {
		rel.TargetClusterID, rel.State = candidates[0], "declared"
	} else if len(candidates) > 1 {
		rel.TargetCandidates = append([]string{}, candidates...)
		rel.State = "ambiguous"
	}
	sourceClient := clients[source.ClusterID]
	if sourceClient == nil {
		rel.State = "unavailable"
		return rel
	}
	if !fleetNamespaceAllowed(namespaces[source.ClusterID], namespace) {
		rel.State = "out_of_scope"
		return rel
	}
	sourceObj, err := sourceClient.Resource(allowed.GVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		rel.State = classifyFleetReadError(err)
		if rel.State == "not_found" {
			rel.State = "unresolved"
		}
		return rel
	}
	if recordedUID != "" && recordedUID != string(sourceObj.GetUID()) {
		rel.State = "unresolved"
		return rel
	}
	rel.SourceResource = ptrFleetResource(fleetTargetFromObject(source, allowed, sourceObj))
	if rel.TargetClusterID == "" {
		return rel
	}
	targetClient := clients[rel.TargetClusterID]
	if targetClient == nil {
		rel.State = "unavailable"
		return rel
	}
	if !fleetNamespaceAllowed(namespaces[rel.TargetClusterID], namespace) {
		rel.State = "out_of_scope"
		return rel
	}
	targetObj, err := targetClient.Resource(allowed.GVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		rel.State = classifyFleetReadError(err)
		if rel.State == "not_found" {
			rel.State = "unresolved"
		}
		return rel
	}
	rel.TargetResource = ptrFleetResource(fleetTargetFromObject(statuses[rel.TargetClusterID], allowed, targetObj))
	rel.State = "observed"
	return rel
}

func ptrFleetResource(r fleetResource) *fleetResource { return &r }

func topologyClusterNode(id string) fleetTopologyNode {
	return fleetTopologyNode{ID: "cluster:" + id, ClusterID: id, Label: id, Type: "cluster"}
}

func (t fleetTools) getFleetTopology(ctx context.Context, _ *mcpsdk.CallToolRequest, input fleetSelection) (*mcpsdk.CallToolResult, fleetTopologyResult, error) {
	_, relationships, err := t.getFleetRelationships(ctx, nil, input)
	if err != nil {
		return nil, fleetTopologyResult{}, err
	}
	selected, err := t.selectClusters(ctx, input)
	if err != nil {
		return nil, fleetTopologyResult{}, err
	}
	return nil, buildFleetTopology(selected, relationships), nil
}

func buildFleetTopology(selected []fleet.ConnectionStatus, relationships fleetRelationshipsResult) fleetTopologyResult {
	result := fleetTopologyResult{Nodes: []fleetTopologyNode{}, Edges: []fleetTopologyEdge{}, Coverage: append([]fleetRelationshipCoverage{}, relationships.Coverage...), Truncated: relationships.Truncated, Limitations: append([]string{}, relationships.Limitations...)}
	nodeIDs := map[string]bool{}
	for _, status := range selected {
		node := topologyClusterNode(status.ClusterID)
		result.Nodes = append(result.Nodes, node)
		nodeIDs[node.ID] = true
	}
	addResourceNode := func(resource *fleetResource) string {
		if resource == nil {
			return ""
		}
		id := "resource:" + resource.ClusterID + ":" + resource.Ref.Key() + ":" + resource.UID
		if !nodeIDs[id] {
			ref := resource.Ref
			result.Nodes = append(result.Nodes, fleetTopologyNode{ID: id, ClusterID: resource.ClusterID, Label: ref.String(), Type: ref.Kind, Ref: &ref, UID: resource.UID})
			nodeIDs[id] = true
		}
		return id
	}
	for _, rel := range relationships.Relationships {
		source := addResourceNode(rel.SourceResource)
		if source == "" {
			source = "cluster:" + rel.SourceClusterID
		}
		evidence := append([]fleetResource{}, rel.Evidence...)
		if rel.SourceResource != nil && rel.TargetResource != nil {
			target := addResourceNode(rel.TargetResource)
			result.Edges = append(result.Edges, fleetTopologyEdge{ID: rel.ID, Source: source, Target: target, Type: rel.Type, State: rel.State, Evidence: evidence})
		} else if rel.TargetClusterID != "" {
			target := "cluster:" + rel.TargetClusterID
			if nodeIDs[source] && nodeIDs[target] {
				result.Edges = append(result.Edges, fleetTopologyEdge{ID: rel.ID, Source: source, Target: target, Type: rel.Type, State: rel.State, Evidence: evidence})
			}
		} else if len(rel.TargetCandidates) > 0 {
			for i, candidate := range rel.TargetCandidates {
				target := "cluster:" + candidate
				if nodeIDs[source] && nodeIDs[target] {
					result.Edges = append(result.Edges, fleetTopologyEdge{ID: rel.ID + ":" + strconv.Itoa(i), Source: source, Target: target, Type: rel.Type, State: rel.State, Evidence: evidence})
				}
			}
		} else {
			target := "unresolved:" + rel.ID
			result.Nodes = append(result.Nodes, fleetTopologyNode{ID: target, Label: rel.MemberName, Type: "unresolved"})
			nodeIDs[target] = true
			if nodeIDs[source] {
				result.Edges = append(result.Edges, fleetTopologyEdge{ID: rel.ID, Source: source, Target: target, Type: rel.Type, State: rel.State, Evidence: evidence})
			}
		}
	}
	return result
}
