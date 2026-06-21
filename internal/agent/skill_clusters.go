package agent

import (
	"sort"

	"github.com/LunaeWaves/Lununda-agent/internal/store"
)

// BuildClusters treats each related SkillPair as an undirected edge and
// returns connected components (clusters) with ≥2 members. Singletons
// are dropped — a skill with no related peers doesn't need synthesis.
// Members within a cluster are sorted; clusters are sorted by first member.
func BuildClusters(edges []store.SkillPair) [][]string {
	uf := newUnionFind()
	for _, e := range edges {
		uf.union(e.A, e.B)
	}
	groups := map[string][]string{}
	for node := range uf.nodes {
		root := uf.find(node)
		groups[root] = append(groups[root], node)
	}
	out := [][]string{}
	for _, g := range groups {
		if len(g) < 2 {
			continue
		}
		sort.Strings(g)
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

type unionFind struct {
	parent map[string]string
	nodes  map[string]bool
}

func newUnionFind() *unionFind {
	return &unionFind{parent: map[string]string{}, nodes: map[string]bool{}}
}

func (u *unionFind) find(x string) string {
	if !u.nodes[x] {
		u.nodes[x] = true
		u.parent[x] = x
	}
	for u.parent[x] != x {
		u.parent[x] = u.parent[u.parent[x]] // path compression
		x = u.parent[x]
	}
	return x
}

func (u *unionFind) union(a, b string) {
	ra, rb := u.find(a), u.find(b)
	if ra != rb {
		u.parent[ra] = rb
	}
}
