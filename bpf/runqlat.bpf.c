#include <linux/bpf.h>
#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>

#define TASK_COMM_LEN 16
#define LATENCY_BUCKETS 64

struct task_struct {
    int pid;
    int tgid;
    char comm[TASK_COMM_LEN];
} __attribute__((preserve_access_index));

struct task_identity {
    __u64 cgroup_id;
    __u32 tgid;
    __u32 reserved;
};

struct latency_key {
    __u64 cgroup_id;
    __u32 tgid;
    __u32 pid;
};

struct histogram_key {
    struct latency_key process;
    __u32 bucket;
    __u32 reserved;
};

struct latency_stats {
    __u64 total_ns;
    __u64 count;
    char comm[TASK_COMM_LEN];
};

struct {
    __uint(type, BPF_MAP_TYPE_LRU_HASH);
    __uint(max_entries, 16384);
    __type(key, __u32);
    __type(value, __u64);
} runnable_since SEC(".maps");

struct {
    __uint(type, BPF_MAP_TYPE_LRU_HASH);
    __uint(max_entries, 16384);
    __type(key, __u32);
    __type(value, struct task_identity);
} task_identity SEC(".maps");

struct {
    __uint(type, BPF_MAP_TYPE_LRU_HASH);
    __uint(max_entries, 4096);
    __type(key, struct latency_key);
    __type(value, struct latency_stats);
} latency_stats SEC(".maps");

struct {
    __uint(type, BPF_MAP_TYPE_LRU_HASH);
    __uint(max_entries, 16384);
    __type(key, struct histogram_key);
    __type(value, __u64);
} latency_histogram SEC(".maps");

static __always_inline __u32 log2_bucket(__u64 value)
{
    __u32 bucket = 0;

    if (value >> 32) {
        value >>= 32;
        bucket += 32;
    }
    if (value >> 16) {
        value >>= 16;
        bucket += 16;
    }
    if (value >> 8) {
        value >>= 8;
        bucket += 8;
    }
    if (value >> 4) {
        value >>= 4;
        bucket += 4;
    }
    if (value >> 2) {
        value >>= 2;
        bucket += 2;
    }
    if (value >> 1)
        bucket += 1;
    return bucket;
}

static __always_inline int record_wakeup(struct task_struct *task)
{
    __u64 now = bpf_ktime_get_ns();
    __u32 pid = BPF_CORE_READ(task, pid);

    if (pid != 0)
        bpf_map_update_elem(&runnable_since, &pid, &now, BPF_NOEXIST);
    return 0;
}

SEC("raw_tracepoint/sched_wakeup")
int trace_wakeup(struct bpf_raw_tracepoint_args *ctx)
{
    return record_wakeup((struct task_struct *)ctx->args[0]);
}

SEC("raw_tracepoint/sched_wakeup_new")
int trace_wakeup_new(struct bpf_raw_tracepoint_args *ctx)
{
    return record_wakeup((struct task_struct *)ctx->args[0]);
}

SEC("raw_tracepoint/sched_switch")
int trace_runqlat(struct bpf_raw_tracepoint_args *ctx)
{
    struct task_struct *prev = (struct task_struct *)ctx->args[1];
    struct task_struct *next = (struct task_struct *)ctx->args[2];
    struct latency_stats initial = {};
    struct task_identity identity = {};
    struct task_identity *next_identity;
    struct histogram_key histogram = {};
    struct latency_stats *stats;
    struct latency_key key = {};
    __u64 pid_tgid;
    __u64 *started;
    __u64 *count;
    __u64 latency_ns;
    __u64 latency_us;
    __u64 now;
    __u64 zero = 0;
    __u32 prev_pid;
    __u32 next_pid;

    now = bpf_ktime_get_ns();
    prev_pid = BPF_CORE_READ(prev, pid);
    next_pid = BPF_CORE_READ(next, pid);
    pid_tgid = bpf_get_current_pid_tgid();

    if (prev_pid != 0 && (__u32)pid_tgid == prev_pid) {
        identity.cgroup_id = bpf_get_current_cgroup_id();
        identity.tgid = BPF_CORE_READ(prev, tgid);
        bpf_map_update_elem(&task_identity, &prev_pid, &identity, BPF_ANY);

        if (ctx->args[3] == 0)
            bpf_map_update_elem(&runnable_since, &prev_pid, &now, BPF_ANY);
    }

    if (next_pid == 0)
        return 0;

    started = bpf_map_lookup_elem(&runnable_since, &next_pid);
    next_identity = bpf_map_lookup_elem(&task_identity, &next_pid);
    if (!started || !next_identity || now <= *started)
        goto cleanup;

    latency_ns = now - *started;
    key.cgroup_id = next_identity->cgroup_id;
    key.tgid = next_identity->tgid;
    key.pid = next_pid;

    stats = bpf_map_lookup_elem(&latency_stats, &key);
    if (!stats) {
        bpf_core_read_str(initial.comm, sizeof(initial.comm), &next->comm);
        bpf_map_update_elem(&latency_stats, &key, &initial, BPF_NOEXIST);
        stats = bpf_map_lookup_elem(&latency_stats, &key);
    }
    if (stats) {
        bpf_core_read_str(stats->comm, sizeof(stats->comm), &next->comm);
        __sync_fetch_and_add(&stats->total_ns, latency_ns);
        __sync_fetch_and_add(&stats->count, 1);
    }

    latency_us = (latency_ns + 999) / 1000;
    if (latency_us == 0)
        latency_us = 1;
    histogram.process = key;
    histogram.bucket = log2_bucket(latency_us);
    count = bpf_map_lookup_elem(&latency_histogram, &histogram);
    if (!count) {
        bpf_map_update_elem(&latency_histogram, &histogram, &zero, BPF_NOEXIST);
        count = bpf_map_lookup_elem(&latency_histogram, &histogram);
    }
    if (count)
        __sync_fetch_and_add(count, 1);

cleanup:
    bpf_map_delete_elem(&runnable_since, &next_pid);
    return 0;
}

char LICENSE[] SEC("license") = "Dual MIT/GPL";
