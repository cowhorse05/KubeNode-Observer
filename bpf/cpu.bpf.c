#include <linux/bpf.h>
#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>

#define TASK_COMM_LEN 16

struct sched_entity {
    __u64 sum_exec_runtime;
} __attribute__((preserve_access_index));

struct task_struct {
    int pid;
    int tgid;
    char comm[TASK_COMM_LEN];
    struct sched_entity se;
} __attribute__((preserve_access_index));

struct cpu_key {
    __u64 cgroup_id;
    __u32 tgid;
    __u32 pid;
};

struct cpu_value {
    __u64 runtime_ns;
    char comm[TASK_COMM_LEN];
};

struct cpu_start {
    __u64 runtime_ns;
    __u32 pid;
    __u32 pad;
};

struct {
    __uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
    __uint(max_entries, 1);
    __type(key, __u32);
    __type(value, struct cpu_start);
} task_start SEC(".maps");

struct {
    __uint(type, BPF_MAP_TYPE_LRU_HASH);
    __uint(max_entries, 8192);
    __type(key, struct cpu_key);
    __type(value, struct cpu_value);
} cpu_runtime SEC(".maps");

SEC("raw_tracepoint/sched_switch")
int trace_cpu_runtime(struct bpf_raw_tracepoint_args *ctx)
{
    struct task_struct *prev = (struct task_struct *)ctx->args[1];
    struct task_struct *next = (struct task_struct *)ctx->args[2];
    struct cpu_value initial = {};
    struct cpu_value *value;
    struct cpu_key key = {};
    __u64 pid_tgid;
    struct cpu_start *started;
    __u64 runtime;
    __u64 delta;
    __u32 prev_pid;
    __u32 next_pid;
    __u32 zero = 0;

    prev_pid = BPF_CORE_READ(prev, pid);
    next_pid = BPF_CORE_READ(next, pid);

    started = bpf_map_lookup_elem(&task_start, &zero);
    runtime = BPF_CORE_READ(prev, se.sum_exec_runtime);
    pid_tgid = bpf_get_current_pid_tgid();
    if (prev_pid != 0 && started && started->pid == prev_pid &&
        runtime >= started->runtime_ns && (__u32)pid_tgid == prev_pid) {
        // Use scheduler-accounted runtime, not time between tracepoint calls.
        // One CPU-local slot follows the incoming task, so PID reuse and LRU
        // eviction cannot corrupt the current slice's baseline.
        delta = runtime - started->runtime_ns;
        key.cgroup_id = bpf_get_current_cgroup_id();
        key.tgid = BPF_CORE_READ(prev, tgid);
        key.pid = prev_pid;

        value = bpf_map_lookup_elem(&cpu_runtime, &key);
        if (!value) {
            initial.runtime_ns = 0;
            bpf_core_read_str(initial.comm, sizeof(initial.comm), &prev->comm);
            bpf_map_update_elem(&cpu_runtime, &key, &initial, BPF_NOEXIST);
            value = bpf_map_lookup_elem(&cpu_runtime, &key);
        }
        if (value) {
            bpf_core_read_str(value->comm, sizeof(value->comm), &prev->comm);
            __sync_fetch_and_add(&value->runtime_ns, delta);
        }
    }

    if (started) {
        started->pid = next_pid;
        started->runtime_ns = BPF_CORE_READ(next, se.sum_exec_runtime);
    }

    return 0;
}

char LICENSE[] SEC("license") = "Dual MIT/GPL";
