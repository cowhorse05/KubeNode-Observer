#include <linux/bpf.h>
#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>

#define TASK_COMM_LEN 16

struct task_struct {
    int pid;
    int tgid;
    char comm[TASK_COMM_LEN];
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

struct {
    __uint(type, BPF_MAP_TYPE_LRU_HASH);
    __uint(max_entries, 32768);
    __type(key, __u32);
    __type(value, __u64);
} task_start SEC(".maps");

struct {
    __uint(type, BPF_MAP_TYPE_LRU_HASH);
    __uint(max_entries, 65536);
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
    __u64 *started;
    __u64 now;
    __u64 delta;
    __u32 prev_pid;
    __u32 next_pid;

    now = bpf_ktime_get_ns();
    prev_pid = BPF_CORE_READ(prev, pid);
    next_pid = BPF_CORE_READ(next, pid);

    started = bpf_map_lookup_elem(&task_start, &prev_pid);
    pid_tgid = bpf_get_current_pid_tgid();
    if (prev_pid != 0 && started && now > *started && (__u32)pid_tgid == prev_pid) {
        delta = now - *started;
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

    if (next_pid != 0)
        bpf_map_update_elem(&task_start, &next_pid, &now, BPF_ANY);

    return 0;
}

char LICENSE[] SEC("license") = "Dual MIT/GPL";
