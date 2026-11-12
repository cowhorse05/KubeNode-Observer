#define _GNU_SOURCE

#include <errno.h>
#include <pthread.h>
#include <sched.h>
#include <stdatomic.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

struct worker {
    int cpu;
    uint64_t value;
    atomic_uint *ready;
    atomic_int *failed;
    atomic_bool *stop;
};

static int seconds_arg(const char *arg, const char *prefix, unsigned *result) {
    size_t prefix_len = strlen(prefix);
    if (strncmp(arg, prefix, prefix_len) != 0) {
        return 0;
    }
    errno = 0;
    char *end = NULL;
    unsigned long value = strtoul(arg + prefix_len, &end, 10);
    if (errno != 0 || end == arg + prefix_len || strcmp(end, "s") != 0 || value > 3600) {
        return -1;
    }
    *result = (unsigned)value;
    return 1;
}

static int sleep_seconds(unsigned seconds) {
    struct timespec remaining = {.tv_sec = (time_t)seconds, .tv_nsec = 0};
    while (nanosleep(&remaining, &remaining) != 0) {
        if (errno != EINTR) {
            return -1;
        }
    }
    return 0;
}

static void *burn(void *opaque) {
    struct worker *worker = opaque;
    cpu_set_t affinity;
    CPU_ZERO(&affinity);
    CPU_SET(worker->cpu, &affinity);
    if (pthread_setaffinity_np(pthread_self(), sizeof(affinity), &affinity) != 0) {
        atomic_store_explicit(worker->failed, 1, memory_order_relaxed);
    }
    atomic_fetch_add_explicit(worker->ready, 1, memory_order_release);

    uint64_t value = worker->value;
    while (!atomic_load_explicit(worker->stop, memory_order_relaxed)) {
        for (unsigned iteration = 0; iteration < 100000; iteration++) {
            value = value * 1664525u + 1013904223u;
        }
        sched_yield();
    }
    worker->value = value;
    return NULL;
}

int main(int argc, char **argv) {
    unsigned workers = 1;
    unsigned warmup = 0;
    unsigned duration = 120;
    for (int index = 1; index < argc; index++) {
        int parsed = seconds_arg(argv[index], "--warmup=", &warmup);
        if (parsed == 0) {
            parsed = seconds_arg(argv[index], "--duration=", &duration);
        }
        if (parsed == 0 && strncmp(argv[index], "--workers=", 10) == 0) {
            errno = 0;
            char *end = NULL;
            unsigned long value = strtoul(argv[index] + 10, &end, 10);
            if (errno != 0 || end == argv[index] + 10 || *end != '\0' || value == 0 || value > 128) {
                parsed = -1;
            } else {
                workers = (unsigned)value;
                parsed = 1;
            }
        }
        if (parsed <= 0) {
            fprintf(stderr, "invalid argument: %s\n", argv[index]);
            return 2;
        }
    }

    cpu_set_t allowed;
    if (sched_getaffinity(0, sizeof(allowed), &allowed) != 0) {
        perror("sched_getaffinity");
        return 1;
    }
    int cpu = -1;
    for (int candidate = 0; candidate < CPU_SETSIZE; candidate++) {
        if (CPU_ISSET(candidate, &allowed)) {
            cpu = candidate;
            break;
        }
    }
    if (cpu < 0) {
        fputs("CPU affinity set is empty\n", stderr);
        return 1;
    }

    pthread_t *threads = calloc(workers, sizeof(*threads));
    struct worker *states = calloc(workers, sizeof(*states));
    if (threads == NULL || states == NULL) {
        fputs("allocate worker state failed\n", stderr);
        free(threads);
        free(states);
        return 1;
    }

    atomic_uint ready = 0;
    atomic_int failed = 0;
    atomic_bool stop = 0;
    unsigned created = 0;
    for (; created < workers; created++) {
        states[created] = (struct worker){.cpu = cpu, .value = created + 1,
                                          .ready = &ready, .failed = &failed, .stop = &stop};
        int error = pthread_create(&threads[created], NULL, burn, &states[created]);
        if (error != 0) {
            fprintf(stderr, "pthread_create: %s\n", strerror(error));
            atomic_store_explicit(&failed, 1, memory_order_relaxed);
            break;
        }
    }
    while (atomic_load_explicit(&ready, memory_order_acquire) < created) {
        sched_yield();
    }
    if (created != workers || atomic_load_explicit(&failed, memory_order_relaxed) != 0
        || sleep_seconds(warmup) != 0) {
        atomic_store_explicit(&stop, 1, memory_order_relaxed);
        for (unsigned index = 0; index < created; index++) {
            pthread_join(threads[index], NULL);
        }
        free(threads);
        free(states);
        return 1;
    }

    printf("running %u worker(s) on CPU %d for %us\n", workers, cpu, duration);
    fflush(stdout);
    int sleep_failed = sleep_seconds(duration) != 0;
    atomic_store_explicit(&stop, 1, memory_order_relaxed);
    for (unsigned index = 0; index < created; index++) {
        pthread_join(threads[index], NULL);
    }
    free(threads);
    free(states);
    return sleep_failed ? 1 : 0;
}
