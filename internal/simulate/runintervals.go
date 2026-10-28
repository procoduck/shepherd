package simulate

import (
	"sort"
	"strings"
)

// The sandbox run window (#424) is extended to cover two of the longest
// scrape or poll intervals in the pipeline. "Interval" here means a cadence
// that gates when the component next produces, collects or delivers data, so
// a run shorter than it can end before anything happens. Retry backoff, TLS
// reloads, garbage collection, WAL housekeeping, health checks and metadata
// caches are cadences too, but none of them delays the first data, so they
// are excluded.
//
// Every duration attribute of every shipped schema whose name ends in
// interval, frequency or period is classified here as counted or excluded,
// with a reason; runwindow_test.go fails when a shipped schema has one that
// is not, or when an entry here matches nothing a shipped schema has (the
// structural pattern of the strict component classification, #421).

// IntervalClass is one interval attribute's classification.
type IntervalClass struct {
	// Counted reports that the attribute is a data cadence the run window
	// covers.
	Counted bool
	// Reason is one line saying why.
	Reason string
	// Key is the classification entry that matched (IntervalClassificationKeys).
	Key string
}

type intervalRule struct {
	counted bool
	reason  string
}

const (
	reasonRetry      = "retry backoff: only matters after a failure, never delays the first data"
	reasonTLSReload  = "how often TLS files are re-read, not a data cadence"
	reasonHousekeep  = "internal housekeeping, not a data cadence"
	reasonK8sCache   = "Kubernetes metadata cache resync, not a data cadence"
	reasonWAL        = "write-ahead log housekeeping or replay tuning, not a data cadence"
	reasonExporterIn = "an exporter's internal refresh; its data leaves on the scrape that reads it, whose scrape_interval is counted"
)

// intervalRules are keyed, most specific first, by "<component> <path>",
// by a block-qualified path suffix ("<block>.<attribute>"), or by the
// attribute name alone.
var intervalRules = map[string]intervalRule{
	// Counted: by name.
	"scrape_interval":         {true, "how often the component scrapes its targets"},
	"default_scrape_interval": {true, "the scrape interval a monitor resource gets when it sets none"},
	"collection_interval":     {true, "how often the receiver collects"},
	"collect_interval":        {true, "how often the collector collects"},
	"refresh_interval":        {true, "how often targets are rediscovered"},
	"poll_frequency":          {true, "how often the source is polled"},
	"poll_interval":           {true, "how often the source is polled"},
	"min_poll_frequency":      {true, "how often a watched file is polled at the fastest"},
	"max_poll_frequency":      {true, "how often a watched file is polled at the slowest"},
	"reread_frequency":        {true, "how often the secret is re-read"},
	"sync_interval":           {true, "how often the rules are synced"},
	"sync_period":             {true, "how often matching files are rediscovered"},
	"metrics_flush_interval":  {true, "how often the connector emits the metrics it derived"},
	"interval":                {true, "how often the component emits"},
	"sampling_interval":       {true, "how often runtime metrics are sampled"},
	"flush_interval":          {true, "how often buffered data is written out"},
	"batch_interval":          {true, "how often persisted samples are batched for sending"},

	// Counted names, excluded where the block says it is not data.
	"health_check.collect_interval":               {false, "a health check of the collector, not a data cadence"},
	"setup_actors.collect_interval":               {false, "a setup check of the database, not a data cadence"},
	"setup_consumers.collect_interval":            {false, "a setup check of the database, not a data cadence"},
	"metadata.refresh_interval":                   {false, "Kafka cluster metadata refresh, not a data cadence"},
	"name_resolver.ecs.refresh_interval":          {false, "name-resolution cache refresh, not a data cadence"},
	"resolver.dns.interval":                       {false, "load-balancer backend resolution, not a data cadence"},
	"resolver.aws_cloud_map.interval":             {false, "load-balancer backend resolution, not a data cadence"},
	"heartbeat.interval":                          {false, "a heartbeat, not a data cadence"},
	"autocommit.interval":                         {false, "Kafka offset autocommit, not a data cadence"},
	"discovery.poll_interval":                     {false, "Beyla's process discovery; the instrumented process's traffic is the data"},
	"endpoint.parallelism.network_flush_interval": {false, "send parallelism tuning, not a data cadence"},
	"update.scrape_interval":                      {false, "how often the Windows update collector refreshes its cache; the exporter's data leaves on the scrape that reads it"},
	"decoupled_scraping.scrape_interval":          {false, reasonExporterIn},

	// Excluded: by name.
	"initial_interval":               {false, reasonRetry},
	"max_interval":                   {false, reasonRetry},
	"start_interval":                 {false, reasonRetry},
	"reconnect_initial_interval":     {false, reasonRetry},
	"min_backoff_period":             {false, reasonRetry},
	"max_backoff_period":             {false, reasonRetry},
	"reload_interval":                {false, reasonTLSReload},
	"check_interval":                 {false, reasonHousekeep},
	"desired_check_interval":         {false, reasonHousekeep},
	"cleanup_check_interval":         {false, reasonHousekeep},
	"error_cleanup_interval":         {false, reasonHousekeep},
	"idle_series_cleanup_interval":   {false, reasonHousekeep},
	"setup_consumers_check_interval": {false, reasonHousekeep},
	"gc_frequency":                   {false, reasonHousekeep},
	"gc_interval":                    {false, reasonHousekeep},
	"heartbeat_interval":             {false, "a Kafka consumer-group heartbeat, not a data cadence"},
	"informers_resync_period":        {false, reasonK8sCache},
	"watch_sync_period":              {false, reasonK8sCache},
	"pod_delete_grace_period":        {false, reasonK8sCache},
	"truncate_frequency":             {false, reasonWAL},
	"min_read_frequency":             {false, reasonWAL},
	"max_read_frequency":             {false, reasonWAL},
	"send_interval":                  {false, "metadata sending only; samples are sent as they arrive"},
	"force_flush_period":             {false, "when a partial last line is flushed, not a read cadence"},
	"event_flush_interval":           {false, reasonExporterIn},
	"clusterinfo_interval":           {false, reasonExporterIn},
	"resctrl_interval":               {false, reasonExporterIn},
	"request_interval":               {false, "the time window each GCP API request covers, not how often the exporter is read"},
	"period":                         {false, "the CloudWatch statistic period, not how often the exporter is read"},
	"listen_poll_period":             {false, "Beyla's internal socket polling, not a data cadence"},
	"bpf_metric_scrape_interval":     {false, "Beyla's own internal metrics, not the pipeline's data"},
}

// ClassifyIntervalAttribute classifies one attribute of a component, by its
// block-qualified path. ok is false when no entry matches.
func ClassifyIntervalAttribute(component string, path []string) (IntervalClass, bool) {
	if len(path) == 0 {
		return IntervalClass{}, false
	}
	full := component + " " + strings.Join(path, ".")
	if r, ok := intervalRules[full]; ok {
		return IntervalClass{Counted: r.counted, Reason: r.reason, Key: full}, true
	}
	for i := 0; i < len(path)-1; i++ {
		suffix := strings.Join(path[i:], ".")
		if r, ok := intervalRules[suffix]; ok {
			return IntervalClass{Counted: r.counted, Reason: r.reason, Key: suffix}, true
		}
	}
	name := path[len(path)-1]
	if r, ok := intervalRules[name]; ok {
		return IntervalClass{Counted: r.counted, Reason: r.reason, Key: name}, true
	}
	return IntervalClass{}, false
}

// IntervalClassificationKeys lists every classification entry, sorted.
func IntervalClassificationKeys() []string {
	keys := make([]string, 0, len(intervalRules))
	for k := range intervalRules {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
