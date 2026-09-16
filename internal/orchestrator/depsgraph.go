package orchestrator

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/geekychris/chief/internal/project"
	"github.com/geekychris/chief/internal/store"
)

// DepsNode is one project in the deps graph.
type DepsNode struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	DependsOn []string `json:"depends_on"` // ids of prerequisite projects
	Blocks    []string `json:"blocks"`     // ids of projects that depend on this one (derived)
}

// DepsGraph is the computed dependency graph across every registered
// project. Cycles are surfaced separately so the caller can render them.
type DepsGraph struct {
	Nodes         []DepsNode `json:"nodes"`
	ReleaseOrder  []string   `json:"release_order,omitempty"` // ids in topological order (roots first); empty if there's a cycle
	Cycles        [][]string `json:"cycles,omitempty"`        // any strongly-connected components with >1 node
	UnknownRefs   []string   `json:"unknown_refs,omitempty"`  // names/ids that appeared in depends_on but weren't found
}

// BuildDepsGraph walks every project's .chief/project.yaml, resolves
// depends_on strings (names OR ids) against known projects, and returns
// the graph + a topological "release order" (Kahn's algorithm).
func BuildDepsGraph(ctx context.Context, st *store.Store, pm *project.Manager) (DepsGraph, error) {
	projs, err := st.ListProjects(ctx)
	if err != nil {
		return DepsGraph{}, err
	}
	byName := map[string]string{} // name → id
	byID := map[string]string{}   // id → name
	for _, p := range projs {
		byName[p.Name] = p.ID
		byID[p.ID] = p.Name
	}

	resolve := func(ref string) (string, bool) {
		if _, ok := byID[ref]; ok {
			return ref, true
		}
		if id, ok := byName[ref]; ok {
			return id, true
		}
		return "", false
	}

	nodes := make(map[string]*DepsNode, len(projs))
	unknown := map[string]struct{}{}
	for _, p := range projs {
		pf, err := pm.ReadYAML(ctx, p.ID)
		if err != nil {
			continue
		}
		deps := make([]string, 0, len(pf.DependsOn))
		for _, ref := range pf.DependsOn {
			if id, ok := resolve(ref); ok && id != p.ID {
				deps = append(deps, id)
			} else if !ok {
				unknown[ref] = struct{}{}
			}
		}
		nodes[p.ID] = &DepsNode{ID: p.ID, Name: p.Name, DependsOn: deps}
	}
	// Derive Blocks (reverse edges).
	for _, n := range nodes {
		for _, dep := range n.DependsOn {
			if target, ok := nodes[dep]; ok {
				target.Blocks = append(target.Blocks, n.ID)
			}
		}
	}
	// Deterministic sort for stable output.
	list := make([]DepsNode, 0, len(nodes))
	for _, n := range nodes {
		sort.Strings(n.DependsOn)
		sort.Strings(n.Blocks)
		list = append(list, *n)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })

	// Kahn's algorithm for topological order.
	inDeg := map[string]int{}
	for _, n := range list {
		inDeg[n.ID] = len(n.DependsOn)
	}
	var queue []string
	for _, n := range list {
		if inDeg[n.ID] == 0 {
			queue = append(queue, n.ID)
		}
	}
	sort.Strings(queue)
	var order []string
	byIDLookup := map[string]DepsNode{}
	for _, n := range list {
		byIDLookup[n.ID] = n
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		order = append(order, id)
		for _, dep := range byIDLookup[id].Blocks {
			inDeg[dep]--
			if inDeg[dep] == 0 {
				queue = append(queue, dep)
			}
		}
		sort.Strings(queue)
	}
	graph := DepsGraph{Nodes: list}
	if len(order) == len(list) {
		graph.ReleaseOrder = order
	} else {
		// Cycle detected — collect the nodes with residual in-degree.
		var cyc []string
		for id, deg := range inDeg {
			if deg > 0 {
				cyc = append(cyc, id)
			}
		}
		sort.Strings(cyc)
		if len(cyc) > 0 {
			graph.Cycles = append(graph.Cycles, cyc)
		}
	}
	for u := range unknown {
		graph.UnknownRefs = append(graph.UnknownRefs, u)
	}
	sort.Strings(graph.UnknownRefs)
	return graph, nil
}

// RenderASCII prints the graph in a human-readable form for the CLI.
func (g DepsGraph) RenderASCII() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Projects: %d\n\n", len(g.Nodes))
	for _, n := range g.Nodes {
		if len(n.DependsOn) == 0 && len(n.Blocks) == 0 {
			fmt.Fprintf(&b, "  %s  (isolated)\n", n.Name)
			continue
		}
		fmt.Fprintf(&b, "  %s\n", n.Name)
		for _, d := range n.DependsOn {
			fmt.Fprintf(&b, "    depends on → %s\n", g.name(d))
		}
		for _, d := range n.Blocks {
			fmt.Fprintf(&b, "    blocks     ← %s\n", g.name(d))
		}
	}
	if len(g.ReleaseOrder) > 0 {
		b.WriteString("\nRelease order (build these first → last):\n")
		for i, id := range g.ReleaseOrder {
			fmt.Fprintf(&b, "  %d. %s\n", i+1, g.name(id))
		}
	}
	if len(g.Cycles) > 0 {
		b.WriteString("\n⚠ Dependency cycles detected:\n")
		for _, c := range g.Cycles {
			names := make([]string, len(c))
			for i, id := range c {
				names[i] = g.name(id)
			}
			fmt.Fprintf(&b, "  %s\n", strings.Join(names, " → "))
		}
	}
	if len(g.UnknownRefs) > 0 {
		fmt.Fprintf(&b, "\nUnknown refs in depends_on: %s\n", strings.Join(g.UnknownRefs, ", "))
	}
	return b.String()
}

func (g DepsGraph) name(id string) string {
	for _, n := range g.Nodes {
		if n.ID == id {
			return n.Name
		}
	}
	return id
}
