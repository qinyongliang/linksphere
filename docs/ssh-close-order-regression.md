# SSH channel write/EOF ordering

Two races found by the broader server race run were reproduced on the unchanged close-order baseline with the same Go toolchain and -race -count=3: the SFTP accepted-then-exited fallback and interactive shell remote-exit path each failed once (six test executions: two failures, four passes).

The fix drains both shell output writers before sending outbound EOF. The stdin copier remains the sole owner of its own half-close; the main goroutine no longer issues a competing close. SFTP runner abort now closes the whole channel directly without a redundant half-close that could race with handshake or proxy writes. No new lock or unbounded join is added around a potentially blocked handshake writer.

Candidate verification: the original two integration regressions plus a new full-close-only unit test passed all nine executions with -race -count=3. The broader related internal SFTP/shell selection passed fifteen executions with the same flags. These are targeted race checks, not a full server-suite pass or proof that adversarial backpressure cannot block a writer. Full Channel.Close is not claimed to synchronously terminate every goroutine.

The earlier full server run still records P2P convergence failures, external OpenSSH configuration rejection, skipped tests and a whole-package 600-second timeout; those remaining issues are not fixed by this patch. No OS security configuration, timeout threshold, real communication provider or device setting changed. Development checkpoint only.
