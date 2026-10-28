package ghapi

import (
	"context"
	"fmt"
	"time"

	"github.com/solcyber/gh-dillodash/internal/model"
)

type fortressNode struct {
	Branches struct {
		TotalCount int `json:"totalCount"`
		Nodes      []*struct {
			Name   string `json:"name"`
			Target struct {
				CommittedDate time.Time `json:"committedDate"`
			} `json:"target"`
		} `json:"nodes"`
	} `json:"branches"`
	DefaultBranchRef *struct {
		Name   string `json:"name"`
		Target struct {
			History struct {
				Nodes []*struct {
					Oid             string    `json:"oid"`
					MessageHeadline string    `json:"messageHeadline"`
					CommittedDate   time.Time `json:"committedDate"`
				} `json:"nodes"`
			} `json:"history"`
		} `json:"target"`
	} `json:"defaultBranchRef"`
	Open struct {
		Nodes []*model.PR `json:"nodes"`
	} `json:"open"`
}

// Fortress reads every repo's newest branches, default-branch history
// and open PRs in one aliased round-trip; a repo the token can't see is
// left out of the map.
func (c *GitHub) Fortress(ctx context.Context, repos []model.Repo) (map[model.Repo]FortressData, error) {
	out := make(map[model.Repo]FortressData, len(repos))
	if len(repos) == 0 {
		return out, nil
	}
	vars := make(map[string]any, 2*len(repos))
	for i, r := range repos {
		vars[fmt.Sprintf("o%d", i)] = r.Owner
		vars[fmt.Sprintf("n%d", i)] = r.Name
	}
	data, _, err := c.graphql(ctx, fortressQuery(len(repos)), vars)
	if err != nil {
		return nil, err
	}
	for i, r := range repos {
		var node fortressNode
		ok, err := data.get(fmt.Sprintf("f%d", i), &node)
		if err != nil {
			return nil, &Error{Message: "bad response: " + err.Error()}
		}
		if !ok {
			continue
		}
		fd := FortressData{BranchTotal: node.Branches.TotalCount}
		for _, b := range node.Branches.Nodes {
			if b != nil {
				fd.Branches = append(fd.Branches, FortressBranch{Name: b.Name, CommittedDate: b.Target.CommittedDate})
			}
		}
		if d := node.DefaultBranchRef; d != nil {
			fd.Default = d.Name
			for _, cm := range d.Target.History.Nodes {
				if cm != nil {
					fd.Commits = append(fd.Commits, FortressCommit{
						Oid: cm.Oid, MessageHeadline: cm.MessageHeadline, CommittedDate: cm.CommittedDate})
				}
			}
		}
		for _, p := range node.Open.Nodes {
			if p != nil {
				p.Repo = r
				fd.OpenPRs = append(fd.OpenPRs, p)
			}
		}
		out[r] = fd
	}
	return out, nil
}
