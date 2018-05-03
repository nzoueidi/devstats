#!/bin/bash
# GHA2DB_SKIPTIME=1 GHA2DB_SKIPLOG=1 GHA2DB_QOUT=1 GHA2DB_DEBUG=1 ./db2influx events_h metrics/shared/events.sql '2018-05-01' '2018-05-03' d desc:time_diff_as_string
# GHA2DB_SKIPTIME=1 GHA2DB_SKIPLOG=1 GHA2DB_QOUT=1 GHA2DB_DEBUG=1 ./db2influx multi_row_multi_column metrics/kubernetes/pr_workload_table.sql '2018-05-01' '2018-05-03' a_29_30 hist,multivalue,annotations_ranges,skip_past
# GHA2DB_SKIPTIME=1 GHA2DB_SKIPLOG=1 GHA2DB_QOUT=1 GHA2DB_DEBUG=1 ./db2influx multi_row_single_column metrics/kubernetes/hist_reviewers.sql '2018-05-01' '2018-05-03' a_29_30 hist,annotations_ranges,skip_past
GHA2DB_PROJECT=kubernetes GHA2DB_SKIPTIME=1 GHA2DB_SKIPLOG=1 GHA2DB_QOUT=1 GHA2DB_DEBUG=1 ./db2influx multi_row_single_column ./metrics/kubernetes/activity_repo_groups.sql '2014-06-01 0' '2018-05-03 20' h multivalue
