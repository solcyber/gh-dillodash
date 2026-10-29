package ghapi

import (
	"fmt"
	"strings"
)

// The rateLimit block every list-shaped query carries for the header
// meter.
const rateLimitField = "rateLimit { limit remaining resetAt }"

// listRepoFragment carries the list query's fields: the viewer's
// repository permission (for config check), open issues and open PRs
// with their head commit's check rollup.
const listRepoFragment = `
fragment ListRepo on Repository {
  viewerPermission
  issues(states: OPEN, first: 100,
         orderBy: {field: UPDATED_AT, direction: DESC}) {
    totalCount
    nodes {
      number title updatedAt createdAt
      author { login }
      labels(first: 6) { nodes { name } }
      assignees(first: 3) { nodes { login } }
      comments { totalCount }
    }
  }
  pullRequests(states: OPEN, last: 100,
               orderBy: {field: CREATED_AT, direction: ASC}) {
    totalCount
    nodes {
      number title isDraft reviewDecision updatedAt createdAt headRefName
      headRefOid additions deletions
      author { login }
      labels(first: 10) { nodes { name } }
      latestOpinionatedReviews(first: 20) {
        nodes { author { login } state }
      }
      commits(last: 1) {
        nodes {
          commit {
            committedDate
            statusCheckRollup {
              state
              contexts(first: 50) {
                totalCount
                nodes {
                  __typename
                  ... on CheckRun { name status conclusion }
                  ... on StatusContext { context state description }
                }
              }
            }
          }
        }
      }
    }
  }
}`

// listQuery is the GraphQL text for n repos: aliases r0..r(n-1), each
// with its own $o%d/$n%d owner and name variables.
func listQuery(n int) string {
	return aliasedQuery(n, "r", "{ ...ListRepo }") + listRepoFragment
}

// aliasedQuery builds "query($o0: String!, $n0: String!, ...) {
// rateLimit {...} <prefix>0: repository(owner: $o0, name: $n0) <body>
// ... }".
func aliasedQuery(n int, prefix, body string) string {
	var decl, aliases strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			decl.WriteString(", ")
		}
		fmt.Fprintf(&decl, "$o%d: String!, $n%d: String!", i, i)
		fmt.Fprintf(&aliases, "\n  %s%d: repository(owner: $o%d, name: $n%d) %s",
			prefix, i, i, i, body)
	}
	return fmt.Sprintf("query(%s) {\n  %s%s\n}\n", decl.String(), rateLimitField, aliases.String())
}

// Fortress mode: each repo's newest branches, default-branch commits and
// open PRs, in one aliased query per refresh.
const fortressFields = `{
    branches: refs(refPrefix: "refs/heads/", first: 100,
      orderBy: {field: TAG_COMMIT_DATE, direction: DESC}) {
      totalCount nodes { name target { ... on Commit { committedDate } } } }
    defaultBranchRef { name target { ... on Commit { history(first: 30) {
      nodes { oid messageHeadline committedDate } } } } }
    open: pullRequests(states: OPEN, first: 50,
                       orderBy: {field: CREATED_AT, direction: DESC}) {
      nodes { number title isDraft createdAt baseRefName headRefName
              reviewDecision author { login } } } }`

// fortressQuery is the GraphQL text for n repos: aliases f0..f(n-1).
func fortressQuery(n int) string {
	return aliasedQuery(n, "f", fortressFields)
}

// issuesWithLabelQuery generalizes the maintenance-window manifest
// query: open issues carrying a label, with their last 100 comments.
const issuesWithLabelQuery = `
query($owner: String!, $name: String!, $label: String!, $first: Int!) {
  repository(owner: $owner, name: $name) {
    issues(states: OPEN, first: $first, labels: [$label],
           orderBy: {field: UPDATED_AT, direction: DESC}) {
      totalCount
      nodes {
        number title body state updatedAt createdAt
        author { login }
        labels(first: 10) { nodes { name } }
        comments(last: 100) {
          totalCount
          nodes { author { login } createdAt body }
        }
      }
    }
  }
}`

const prCommentsQuery = `
query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      comments(last: 100) { nodes { author { login } createdAt body } }
    }
  }
}`

const issueDetailQuery = `
query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    issue(number: $number) {
      number title body createdAt updatedAt state
      author { login }
      labels(first: 10) { nodes { name } }
      assignees(first: 10) { nodes { login } }
      comments(last: 100) {
        totalCount
        nodes { author { login } createdAt body }
      }
    }
  }
}`

const usersQuery = `
query($owner: String!, $name: String!) {
  repository(owner: $owner, name: $name) {
    assignableUsers(first: 50) { nodes { login name } }
  }
}`

const prDetailQuery = `
query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      number title body isDraft reviewDecision baseRefName headRefName
      headRefOid state additions deletions changedFiles updatedAt createdAt
      author { login }
      labels(first: 10) { nodes { name } }
      commits(last: 1) { nodes { commit { committedDate } } }
      latestOpinionatedReviews(first: 20) {
        nodes { author { login } state }
      }
      comments(last: 100) {
        totalCount
        nodes { author { login } createdAt body }
      }
      reviews(last: 50) {
        totalCount
        nodes { author { login } state submittedAt body }
      }
    }
  }
}`

// Application releases: every repo releases by an approved PR into the
// production branch. The branch names travel as variables — $prod and
// $stage for pullRequests(baseRefName:), $prodRef and $stageRef (their
// refs/heads/ forms) for ref(qualifiedName:). Open PRs into prod plus
// the few most recently merged; one query per repo, so a slow repo
// cannot time out the others.
const releaseQuery = `
query($owner: String!, $name: String!, $prod: String!,
      $prodRef: String!, $stageRef: String!) {
  rateLimit { limit remaining resetAt }
  r0: repository(owner: $owner, name: $name) {
    nameWithOwner isArchived
    prod: ref(qualifiedName: $prodRef) { target { oid } }
    stage: ref(qualifiedName: $stageRef) { target { oid } }
    open: pullRequests(states: [OPEN], baseRefName: $prod, first: 20,
                       orderBy: {field: UPDATED_AT, direction: DESC}) {
      totalCount nodes { ...RelPR } }
    merged: pullRequests(states: [MERGED], baseRefName: $prod, first: 5,
                         orderBy: {field: UPDATED_AT, direction: DESC}) {
      nodes { ...RelPR } }
  }
}
fragment RelPR on PullRequest {
  number title isDraft reviewDecision updatedAt createdAt
  headRefName headRefOid
  state mergeStateStatus mergedAt mergeCommit { oid }
  baseRefName additions deletions
  author { login }
  labels(first: 10) { nodes { name } }
  latestOpinionatedReviews(first: 20) { nodes { author { login } state } }
  commits(last: 1) { nodes { commit { committedDate statusCheckRollup {
    state contexts(first: 50) { totalCount nodes { __typename
      ... on CheckRun { name status conclusion }
      ... on StatusContext { context state description } } } } } } }
}`

// refName is the refs/heads/ form of a branch name, for ref(qualifiedName:).
func refName(branch string) string { return "refs/heads/" + branch }
