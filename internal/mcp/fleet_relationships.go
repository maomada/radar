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
	result := fleetRelationshipsResult{
		Relationships: []fleetRelationship{}, Coverage: []fleetRelationshipCoverage{},
		Limitations: []string{"observed endpoint resources do not prove delivery or ownership"},
	}
	coverageIndex := map[string]int{}
	coverageKey := func(clusterID, resource string) string { return clusterID + "\x00" + resource }
	setCoverage := func(clusterID, resource, state string) {
		if index, ok := coverageIndex[coverageKey(clusterID, resource)]; ok {
			result.Coverage[index].State = state
		}
	}
	resources := []string{"identity", "clusters", "resourcebindings", "clusterresourcebindings"}
	for _, status := range selected {
		for _, resource := range resources {
			coverageIndex[coverageKey(status.ClusterID, resource)] = len(result.Coverage)
			result.Coverage = append(result.Coverage, fleetRelationshipCoverage{ClusterID: status.ClusterID, Resource: resource, State: "not_collected"})
		}
	}
	freshUIDs := map[string][]string{}
	memberTargets := map[string][]string{}
	clients := map[string]dynamic.Interface{}
	namespacesByCluster := map[string][]string{}
	statusesByID := map[string]fleet.ConnectionStatus{}
	for _, status := range selected {
		if ctx.Err() != nil {
			result.Limitations = append(result.Limitations, "relationship collection deadline was reached")
			return nil, result, nil
		}
		statusesByID[status.ClusterID] = status
		typed, dynamic, namespaces, err := t.clients(ctx, status.ClusterID)
		if err != nil {
			for _, resource := range resources {
				setCoverage(status.ClusterID, resource, "unavailable")
			}
			continue
		}
		clients[status.ClusterID], namespacesByCluster[status.ClusterID] = dynamic, namespaces
		if !fleetNamespaceAllowed(namespaces, "kube-system") {
			setCoverage(status.ClusterID, "identity", "out_of_scope")
			continue
		}
		ns, err := typed.CoreV1().Namespaces().Get(ctx, "kube-system", metav1.GetOptions{})
		if err == nil {
			if ns.UID == "" {
				setCoverage(status.ClusterID, "identity", "unresolved")
			} else {
				freshUIDs[string(ns.UID)] = append(freshUIDs[string(ns.UID)], status.ClusterID)
				setCoverage(status.ClusterID, "identity", "complete")
			}
		} else {
			setCoverage(status.ClusterID, "identity", classifyFleetReadError(err))
		}
	}
	if ctx.Err() != nil {
		result.Limitations = append(result.Limitations, "relationship collection deadline was reached")
		return nil, result, nil
	}
	for _, status := range selected {
		if ctx.Err() != nil {
			result.Limitations = append(result.Limitations, "relationship collection deadline was reached")
			return nil, result, nil
		}
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
			setCoverage(status.ClusterID, "clusters", state)
		} else {
			clusterItems := list.Items
			partial := list.GetContinue() != ""
			if len(clusterItems) > 200 {
				clusterItems = clusterItems[:200]
				partial = true
			}
			if partial {
				result.Truncated = true
			}
			for _, member := range clusterItems {
				if ctx.Err() != nil {
					partial = true
					result.Truncated = true
					break
				}
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
					setCoverage(status.ClusterID, "clusters", "partial")
					result.Truncated = true
					result.Limitations = append(result.Limitations, "relationship results were truncated at the global limit")
					return nil, result, nil
				}
			}
			if partial {
				setCoverage(status.ClusterID, "clusters", "partial")
				result.Limitations = append(result.Limitations, "one or more relationship lists were truncated")
			} else {
				setCoverage(status.ClusterID, "clusters", "complete")
			}
		}
		for _, gvr := range []struct {
			gvr  schema.GroupVersionResource
			kind string
		}{{karmadaResourceBindingsGVR, "ResourceBinding"}, {karmadaClusterResourceBindingsGVR, "ClusterResourceBinding"}} {
			if ctx.Err() != nil {
				result.Limitations = append(result.Limitations, "relationship collection deadline was reached")
				return nil, result, nil
			}
			bindingNamespaces := []string{""}
			if gvr.gvr.Resource == karmadaResourceBindingsGVR.Resource && len(namespacesByCluster[status.ClusterID]) > 0 {
				bindingNamespaces = append([]string{}, namespacesByCluster[status.ClusterID]...)
			}
			successes := 0
			failedStates := []string{}
			partial := false
			for _, namespace := range bindingNamespaces {
				if ctx.Err() != nil {
					result.Limitations = append(result.Limitations, "relationship collection deadline was reached")
					if successes > 0 {
						partial = true
					}
					break
				}
				bindings, bindErr := dynamic.Resource(gvr.gvr).Namespace(namespace).List(ctx, metav1.ListOptions{Limit: 200})
				if bindErr != nil {
					state := classifyFleetReadError(bindErr)
					if state == "not_found" {
						state = "unsupported"
					}
					failedStates = append(failedStates, state)
					continue
				}
				successes++
				bindingItems := bindings.Items
				if bindings.GetContinue() != "" || len(bindingItems) > 200 {
					partial = true
					result.Truncated = true
					result.Limitations = append(result.Limitations, "one or more relationship lists were truncated")
				}
				if len(bindingItems) > 200 {
					bindingItems = bindingItems[:200]
				}
				for _, binding := range bindingItems {
					if ctx.Err() != nil {
						partial = true
						result.Truncated = true
						result.Limitations = append(result.Limitations, "relationship collection deadline was reached")
						break
					}
					members, _, _ := unstructured.NestedSlice(binding.Object, "spec", "clusters")
					for _, rawMember := range members {
						if ctx.Err() != nil {
							partial = true
							result.Truncated = true
							result.Limitations = append(result.Limitations, "relationship collection deadline was reached")
							break
						}
						member, ok := rawMember.(map[string]any)
						if !ok {
							continue
						}
						memberName, _ := member["name"].(string)
						rel := t.bindingRelationship(ctx, status, gvr.kind, &binding, memberName, clients, namespacesByCluster, memberTargets, statusesByID)
						if rel != nil {
							result.Relationships = append(result.Relationships, *rel)
							if len(result.Relationships) >= 200 {
								setCoverage(status.ClusterID, gvr.gvr.Resource, "partial")
								result.Truncated = true
								result.Limitations = append(result.Limitations, "relationship results were truncated at the global limit")
								return nil, result, nil
							}
						}
					}
					if ctx.Err() != nil {
						break
					}
				}
			}
			coverageState := "complete"
			if successes > 0 && len(failedStates) > 0 {
				coverageState = "partial"
			} else if successes == 0 && len(failedStates) > 0 {
				coverageState = failedStates[0]
				for _, failedState := range failedStates[1:] {
					if failedState != coverageState {
						coverageState = "partial"
						break
					}
				}
			}
			if partial {
				coverageState = "partial"
			}
			if successes == 0 && len(failedStates) == 0 && ctx.Err() != nil {
				coverageState = "not_collected"
			}
			setCoverage(status.ClusterID, gvr.gvr.Resource, coverageState)
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
