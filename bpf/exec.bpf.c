#include <linux/bpf.h>
#include <bpf/bpf_helpers.h>

#define TASK_COMM_LEN 16

struct exec_event {
    __u64 timestamp_ns;
    __u64 cgroup_id; //current cgroup ID, which is the ID of the cgroup that the process belongs to,use to mark container boundaries
    __u32 pid; //Current thread ID, pid from the kernel's perspective
    __u32 tgid; //Thread group ID, which is the process ID that user-space getpid() sees
    char comm[TASK_COMM_LEN]; //current thread's command name
};

struct {
    __uint(type, BPF_MAP_TYPE_RINGBUF);
    __uint(max_entries, 1 << 20);
} exec_events SEC(".maps");

SEC("tracepoint/sched/sched_process_exec")
int trace_exec(void *ctx)
{
    struct exec_event *event;
    __u64 pid_tgid;

    event = bpf_ringbuf_reserve(&exec_events, sizeof(*event), 0);
    if (!event)
        return 0;

    pid_tgid = bpf_get_current_pid_tgid();
    event->timestamp_ns = bpf_ktime_get_ns();
    event->cgroup_id = bpf_get_current_cgroup_id();
    event->pid = (__u32)pid_tgid;
    event->tgid = (__u32)(pid_tgid >> 32);
    bpf_get_current_comm(&event->comm, sizeof(event->comm));

    bpf_ringbuf_submit(event, 0);
    return 0;
}

char LICENSE[] SEC("license") = "Dual MIT/GPL";

