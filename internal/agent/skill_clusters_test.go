package agent

import (
	"reflect"
	"sort"
	"testing"

	"github.com/LunaeWaves/Lununda-agent/internal/store"
)

func TestBuildClusters(t *testing.T) {
	// 边：docx-pdf, pdf-xlsx（相关）→ {docx,pdf,xlsx} 一个簇
	//     deploy-summary（相关）→ {deploy,summary} 一个簇
	//     isolated 无边 → 不出现
	edges := []store.SkillPair{
		{A: "docx-extract", B: "pdf-extract"},
		{A: "pdf-extract", B: "xlsx-extract"},
		{A: "deploy", B: "summarize-meeting"},
	}
	clusters := BuildClusters(edges)

	got := make([][]string, len(clusters))
	for i, c := range clusters {
		sorted := append([]string(nil), c...)
		sort.Strings(sorted)
		got[i] = sorted
	}
	sort.Slice(got, func(i, j int) bool { return got[i][0] < got[j][0] })

	want := [][]string{
		{"deploy", "summarize-meeting"},
		{"docx-extract", "pdf-extract", "xlsx-extract"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("clusters = %v, want %v", got, want)
	}

	if c := BuildClusters(nil); len(c) != 0 {
		t.Errorf("空边应返回空，got %v", c)
	}
}
