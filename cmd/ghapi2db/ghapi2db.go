package main

import (
	"fmt"
	"io/ioutil"
	"strings"
	"time"

	lib "devstats"

	"github.com/google/go-github/github"
)

type issueConfig struct {
	repo        string
	number      int
	issueID     int
	pr          bool
	milestoneID *int64
}

// Insert Postgres vars
func ghapi() {
	// Environment context parse
	var ctx lib.Ctx
	ctx.Init()

	// Connect to Postgres DB
	c := lib.PgConn(&ctx)
	defer func() { lib.FatalOnError(c.Close()) }()

	// Connect to GitHub API
	gctx, gc := lib.GHClient(&ctx)

	// Get RateLimits info
	_, rem, wait := lib.GetRateLimits(gctx, gc, true)

	// Get number of CPUs available
	thrN := lib.GetThreadsNum(&ctx)
	lib.Printf("ghapi2db.go: Running (on %d CPUs): %d API points available, resets in %v\n", thrN, rem, wait)

	// Local or cron mode?
	dataPrefix := lib.DataDir
	if ctx.Local {
		dataPrefix = "./"
	}
	// Get recently modified opened issues/PRs
	bytes, err := ioutil.ReadFile(
		dataPrefix + "util_sql/open_issues_and_prs.sql",
	)
	lib.FatalOnError(err)
	sqlQuery := string(bytes)

	// Set range from a context
	sqlQuery = strings.Replace(sqlQuery, "{{period}}", ctx.RecentRange, -1)
	rows := lib.QuerySQLWithErr(c, &ctx, sqlQuery)
	defer func() { lib.FatalOnError(rows.Close()) }()

	// Get issues/PRs to check
	// repo, number, issueID, is_pr
	repo, number, issueID, pr := "", 0, 0, false
	m := make(map[int]issueConfig)
	nIssues := 0
	for rows.Next() {
		lib.FatalOnError(rows.Scan(&repo, &number, &issueID, &pr))
		cfg := issueConfig{repo: repo, number: number, issueID: issueID, pr: pr}
		v, ok := m[issueID]
		if ok {
			if ctx.Debug > 0 {
				lib.Printf("Warning: we already have issue config for id=%d: %v, skipped new config: %v\n", issueID, v, cfg)
			}
			continue
		}
		m[issueID] = cfg
		nIssues++
	}
	lib.FatalOnError(rows.Err())

	// GitHub don't like MT quering - they say that:
	// 403 You have triggered an abuse detection mechanism. Please wait a few minutes before you try again
	// So let's get all GitHub stuff one-after-another (ugly and slow) and then spawn threads to speedup
	// Damn GitHub! - this could be working Number of CPU times faster! We're trying some hardcoded value: allowedThrN
	// Seems like GitHub is not detecting abuse when using 16 thread, but it detects when using 32.
	allowedThrN := 16
	if allowedThrN > thrN {
		allowedThrN = thrN
	}
	ch := make(chan bool)
	nThreads := 0
	dtStart := time.Now()
	lastTime := dtStart
	checked := 0
	lib.Printf("ghapi2db.go: Processing %d issues - GHAPI part\n", nIssues)
	for key := range m {
		go func(ch chan bool, iid int) {
			// Refer to current tag using index passed to anonymous function
			cfg := m[iid]
			if ctx.Debug > 0 {
				lib.Printf("GitHub Issue ID '%d' --> '%v'\n", iid, cfg)
			}
			// Get separate org and repo
			ary := strings.Split(cfg.repo, "/")
			if len(ary) != 2 {
				if ctx.Debug > 0 {
					lib.Printf("warning: wrong repository name: %s\n", cfg.repo)
				}
				return
			}
			// Use Github API to get issue info
			issue, _, err := gc.Issues.Get(gctx, ary[0], ary[1], cfg.number)
			if err != nil {
				_, rate := err.(*github.RateLimitError)
				_, abuse := err.(*github.AbuseRateLimitError)
				if abuse || rate {
					// TODO: do something more accurate here
					lib.Printf("Hit rate limit on Issues.Get for %s #%d (id=%d)\n", cfg.repo, cfg.number, cfg.issueID)
				}
				lib.FatalOnError(err)
			}
			if issue.Milestone != nil {
				cfg.milestoneID = issue.Milestone.ID
			}

			// Synchronize go routine
			if ch != nil {
				ch <- true
			}
		}(ch, key)
		// go routine called with 'ch' channel to sync and tag index

		nThreads++
		if nThreads == allowedThrN {
			<-ch
			nThreads--
			checked++
			// Get RateLimits info
			_, rem, wait := lib.GetRateLimits(gctx, gc, true)
			lib.ProgressInfo(checked, nIssues, dtStart, &lastTime, time.Duration(10)*time.Second, fmt.Sprintf("API points: %d, resets in: %v", rem, wait))
		}
	}
	// Usually all work happens on '<-ch'
	lib.Printf("Final GHAPI threads join\n")
	for nThreads > 0 {
		<-ch
		nThreads--
		checked++
		// Get RateLimits info
		_, rem, wait := lib.GetRateLimits(gctx, gc, true)
		lib.ProgressInfo(checked, nIssues, dtStart, &lastTime, time.Duration(10)*time.Second, fmt.Sprintf("API points: %d, resets in: %v", rem, wait))
	}

	// Now iterate all issues/PR in MT mode
	ch = make(chan bool)
	nThreads = 0
	dtStart = time.Now()
	lastTime = dtStart
	checked = 0
	updates := 0
	lib.Printf("ghapi2db.go: Processing %d issues - GHA part\n", nIssues)
	// Use map key to pass to the closure
	for key := range m {
		go func(ch chan bool, iid int) {
			// Refer to current tag using index passed to anonymous function
			cfg := m[iid]
			if ctx.Debug > 0 {
				lib.Printf("GHA Issue ID '%d' --> '%v'\n", iid, cfg)
			}
			var (
				ghaMilestoneID *int64
				ghaEventID     int
			)
			apiMilestoneID := cfg.milestoneID
			rows := lib.QuerySQLWithErr(
				c,
				&ctx,
				fmt.Sprintf("select milestone_id, event_id from gha_issues where id = %s order by event_id desc limit 1", lib.NValue(1)),
				cfg.issueID,
			)
			defer func() { lib.FatalOnError(rows.Close()) }()
			for rows.Next() {
				lib.FatalOnError(rows.Scan(&ghaMilestoneID, &ghaEventID))
			}
			lib.FatalOnError(rows.Err())

			// update will be non-empty when we detect that something needs to be updated
			update := ""
			if apiMilestoneID == nil && ghaMilestoneID != nil {
				update = fmt.Sprintf("update gha_issues set milestone_id = null where id = %s and event_id = %s", lib.NValue(1), lib.NValue(2))
				if ctx.Debug > 0 {
					lib.Printf("Updating issue '%v' milestone to null, it was %d\n", cfg, *ghaMilestoneID)
				}
			}
			if apiMilestoneID != nil && (ghaMilestoneID == nil || *apiMilestoneID != *ghaMilestoneID) {
				update = fmt.Sprintf("update gha_issues set milestone_id = %d where id = %s and event_id = %s", *apiMilestoneID, lib.NValue(1), lib.NValue(2))
				if ctx.Debug > 0 {
					if ghaMilestoneID != nil {
						lib.Printf("Updating issue '%v' milestone to %d, it was %d\n", cfg, *apiMilestoneID, *ghaMilestoneID)
					} else {
						lib.Printf("Updating issue '%v' milestone to %d, it was null\n", cfg, *apiMilestoneID)
					}
				}
			}
			// Do the update if needed
			if update != "" {
				lib.ExecSQLWithErr(c, &ctx, update, lib.AnyArray{cfg.issueID, ghaEventID}...)
				updates++
			}

			// Synchronize go routine
			if ch != nil {
				ch <- true
			}
		}(ch, key)

		// go routine called with 'ch' channel to sync and tag index
		nThreads++
		if nThreads == thrN {
			<-ch
			nThreads--
			checked++
			lib.ProgressInfo(checked, nIssues, dtStart, &lastTime, time.Duration(10)*time.Second, "")
		}
	}
	// Usually all work happens on '<-ch'
	lib.Printf("Final GHA threads join\n")
	for nThreads > 0 {
		<-ch
		nThreads--
		checked++
		lib.ProgressInfo(checked, nIssues, dtStart, &lastTime, time.Duration(10)*time.Second, "")
	}
	// Get RateLimits info
	_, rem, wait = lib.GetRateLimits(gctx, gc, true)
	lib.Printf("ghapi2db.go: Processed %d issues/PRs (%d updated): %d API points remain, resets in %v\n", checked, updates, rem, wait)
}

func main() {
	dtStart := time.Now()
	ghapi()
	dtEnd := time.Now()
	lib.Printf("Time: %v\n", dtEnd.Sub(dtStart))
}