// Ограниченный исследовательский стенд; не launcher для недоверенного WASM.
#include <errno.h>
#include <mach/mach.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mman.h>
#include <sys/resource.h>
#include <sys/wait.h>
#include <unistd.h>

#define MIB (1024ULL * 1024ULL)

struct result {
    int set_error;
    int map_error;
    int task_error;
    uint64_t requested_limit;
    uint64_t baseline_virtual;
    uint64_t baseline_resident;
    uint64_t limit;
    uint64_t allocated;
    uint64_t resident;
};

static struct result allocate(int resource, size_t bytes, uint64_t requested) {
    struct result r = {0};
    mach_task_basic_info_data_t baseline;
    mach_msg_type_number_t initial_count = MACH_TASK_BASIC_INFO_COUNT;
    r.task_error = task_info(mach_task_self(), MACH_TASK_BASIC_INFO,
                            (task_info_t)&baseline, &initial_count);
    if (r.task_error != KERN_SUCCESS) return r;
    r.baseline_virtual = baseline.virtual_size;
    r.baseline_resident = baseline.resident_size;
    r.requested_limit = requested ? requested : baseline.virtual_size + 64 * MIB;
    struct rlimit limit = {r.requested_limit, r.requested_limit};
    if (setrlimit(resource, &limit) != 0) {
        r.set_error = errno;
        return r;
    }
    if (getrlimit(resource, &limit) != 0) {
        r.set_error = errno;
        return r;
    }
    r.limit = limit.rlim_cur;
    volatile uint32_t *data = mmap(NULL, bytes, PROT_READ | PROT_WRITE,
                          MAP_PRIVATE | MAP_ANON, -1, 0);
    if (data == MAP_FAILED) {
        r.map_error = errno;
        return r;
    }
    // Заполняем все страницы неповторяющимися данными, а не только резервируем VA.
    uint32_t value = 0x9137a15bu;
    for (size_t i = 0; i < bytes / sizeof(*data); i++) {
        value ^= value << 13;
        value ^= value >> 17;
        value ^= value << 5;
        data[i] = value;
    }
    r.allocated = bytes;
    mach_task_basic_info_data_t info;
    mach_msg_type_number_t count = MACH_TASK_BASIC_INFO_COUNT;
    r.task_error = task_info(mach_task_self(), MACH_TASK_BASIC_INFO,
                            (task_info_t)&info, &count);
    if (r.task_error == KERN_SUCCESS) r.resident = info.resident_size;
    // Память сохраняется до завершения процесса, в том числе во время барьера.
    return r;
}

static void print_result(struct result r) {
    printf("{\"set_errno\":%d,\"mmap_errno\":%d,\"task_error\":%d,"
           "\"requested_limit\":%llu,\"baseline_virtual\":%llu,\"baseline_resident\":%llu,"
           "\"limit\":%llu,\"allocated\":%llu,\"resident\":%llu}",
           r.set_error, r.map_error, r.task_error,
           (unsigned long long)r.requested_limit, (unsigned long long)r.baseline_virtual,
           (unsigned long long)r.baseline_resident, (unsigned long long)r.limit, (unsigned long long)r.allocated,
           (unsigned long long)r.resident);
}

static int aggregate(uint64_t requested) {
    int results[2], release[2];
    if (pipe(results) != 0 || pipe(release) != 0) return 2;
    pid_t children[2];
    for (int i = 0; i < 2; i++) {
        children[i] = fork();
        if (children[i] < 0) return 3;
        if (children[i] == 0) {
            close(results[0]);
            close(release[1]);
            struct result r = allocate(RLIMIT_AS, 48 * MIB, requested);
            if (write(results[1], &r, sizeof(r)) != (ssize_t)sizeof(r)) _exit(4);
            char token;
            if (read(release[0], &token, 1) != 1) _exit(5);
            _exit(0);
        }
    }
    close(results[1]);
    close(release[0]);
    struct result r[2];
    for (int i = 0; i < 2; i++) {
        if (read(results[0], &r[i], sizeof(r[i])) != (ssize_t)sizeof(r[i])) return 6;
    }
    // Обе записи получены, оба процесса ещё держат память до разрешения выхода.
    printf("{\"mode\":\"aggregate\",\"children\":[");
    print_result(r[0]); printf(","); print_result(r[1]);
    printf("],\"resident_sum\":%llu}\n", (unsigned long long)(r[0].resident + r[1].resident));
    if (write(release[1], "xx", 2) != 2) return 7;
    for (int i = 0; i < 2; i++) {
        int status;
        if (waitpid(children[i], &status, 0) < 0 || !WIFEXITED(status) || WEXITSTATUS(status) != 0) return 8;
    }
    return 0;
}

int main(int argc, char **argv) {
    alarm(10);
    if (argc != 3) return 1;
    uint64_t requested;
    if (strcmp(argv[2], "64") == 0) requested = 64 * MIB;
    else if (strcmp(argv[2], "256") == 0) requested = 256 * MIB;
    else if (strcmp(argv[2], "512") == 0) requested = 512 * MIB;
    else if (strcmp(argv[2], "baseline") == 0) requested = 0;
    else return 1;
    if (strcmp(argv[1], "aggregate") == 0) return aggregate(requested);
    int resource;
    if (strcmp(argv[1], "as") == 0) resource = RLIMIT_AS;
    else if (strcmp(argv[1], "data") == 0) resource = RLIMIT_DATA;
    else return 1;
    printf("{\"mode\":\"%s\",\"rss_alias_as\":%s,\"sample\":", argv[1],
           RLIMIT_RSS == RLIMIT_AS ? "true" : "false");
    print_result(allocate(resource, 96 * MIB, requested));
    printf("}\n");
    return 0;
}
