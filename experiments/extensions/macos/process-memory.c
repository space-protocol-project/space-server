#include <errno.h>
#include <libproc.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <sys/resource.h>

// Только чтение сведений о конкретном процессе стенда.
int main(int argc, char **argv) {
    if (argc != 2) return 2;
    char *end = NULL;
    long pid = strtol(argv[1], &end, 10);
    if (!end || *end || pid <= 0 || pid > INT32_MAX) return 2;
    struct rusage_info_v4 usage = {0};
    int result = proc_pid_rusage((int)pid, RUSAGE_INFO_V4, (rusage_info_t *)&usage);
    int error = result == 0 ? 0 : errno;
    printf("{\"pid\":%ld,\"errno\":%d,\"resident_bytes\":%llu,"
           "\"footprint_bytes\":%llu,\"start_abstime\":%llu}\n",
           pid, error, (unsigned long long)usage.ri_resident_size,
           (unsigned long long)usage.ri_phys_footprint,
           (unsigned long long)usage.ri_proc_start_abstime);
    return 0;
}
