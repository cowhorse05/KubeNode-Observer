/* Hermetic x86_64 test fixture: no args = true; any arg = sleep 200ms.
 * Allows the existing root BPF tests to run in scratch without libc/shell. */
.global _start
.text
_start:
    cmpq $1, (%rsp)
    je done
    mov $35, %eax
    lea pause_time(%rip), %rdi
    xor %esi, %esi
    syscall
done:
    mov $60, %eax
    xor %edi, %edi
    syscall
.section .rodata
pause_time:
    .quad 0, 200000000
.section .note.GNU-stack,"",@progbits
