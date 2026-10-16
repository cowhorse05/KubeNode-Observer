#include <linux/bpf.h>
#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>

#define TASK_COMM_LEN 16
#define EVENT_EXEC 1
#define EVENT_EXIT 2

struct task_struct {
    struct task_struct *real_parent;
    int exit_code;
    int pid;
    int tgid;
} __attribute__((preserve_access_index));

struct exec_event {
    __u64 timestamp_ns;
    __u64 cgroup_id; // Current cgroup ID, used as the container attribution key.
    __u32 pid;       // Kernel thread ID.
    __u32 tgid;      // Thread group ID, equivalent to user-space getpid().
    __u32 ppid;
    __u32 event_type;
    __s32 exit_code;
    char comm[TASK_COMM_LEN];
    __u32 reserved;
};

struct {
    __uint(type, BPF_MAP_TYPE_RINGBUF);
    __uint(max_entries, 1 << 20);
} exec_events SEC(".maps");

struct {
    __uint(type, BPF_MAP_TYPE_ARRAY);
    __uint(max_entries, 1);
    __type(key, __u32);
    __type(value, __u64);
} event_stats SEC(".maps");

static __always_inline void record_lost_event(void)
{
    __u32 key = 0;
    __u64 *lost = bpf_map_lookup_elem(&event_stats, &key);

    if (lost)
        __sync_fetch_and_add(lost, 1);
}

static __always_inline int submit_event(__u32 event_type)
{
    struct exec_event *event;
    struct task_struct *task;
    __u64 pid_tgid;

    event = bpf_ringbuf_reserve(&exec_events, sizeof(*event), 0);
    if (!event) {
        record_lost_event();
        return 0;
    }

    pid_tgid = bpf_get_current_pid_tgid();
    task = (struct task_struct *)bpf_get_current_task_btf();
    event->timestamp_ns = bpf_ktime_get_ns();
    event->cgroup_id = bpf_get_current_cgroup_id();
    event->pid = (__u32)pid_tgid;
    event->tgid = (__u32)(pid_tgid >> 32);
    event->ppid = BPF_CORE_READ(task, real_parent, tgid);
    event->event_type = event_type;
    event->exit_code = event_type == EVENT_EXIT ? BPF_CORE_READ(task, exit_code) : 0;
    event->reserved = 0;
    bpf_get_current_comm(&event->comm, sizeof(event->comm));

    bpf_ringbuf_submit(event, 0);
    return 0;
}

SEC("tracepoint/sched/sched_process_exec")
int trace_exec(void *ctx)
{
    return submit_event(EVENT_EXEC);
}

SEC("tracepoint/sched/sched_process_exit")
int trace_exit(void *ctx)
{
    __u64 pid_tgid = bpf_get_current_pid_tgid();

    if ((__u32)pid_tgid != (__u32)(pid_tgid >> 32))
        return 0;
    return submit_event(EVENT_EXIT);
}

char LICENSE[] SEC("license") = "Dual MIT/GPL";

