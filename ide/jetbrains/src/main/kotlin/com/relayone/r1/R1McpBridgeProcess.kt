package com.relayone.r1

// R1McpBridgeProcess — spawns `r1 mcp serve` and bridges JSON-RPC
// frames between the IDE's AI Assistant MCP client and the child's
// stdin/stdout. Frames follow the MCP stdio framing: one JSON-RPC
// envelope per line (newline-terminated).
//
// The class is intentionally small: spawn(), send(), recv(),
// terminate(). Heavier protocol logic (handshake, capabilities)
// lives in the AI Assistant client; this class is the transport.
//
// Spec: specs/mcp-ide-bundles.md §T6.2.

import com.intellij.openapi.diagnostic.Logger
import java.io.BufferedReader
import java.io.BufferedWriter
import java.io.IOException
import java.io.InputStreamReader
import java.io.OutputStreamWriter
import java.nio.charset.StandardCharsets
import java.util.concurrent.TimeUnit

class R1McpBridgeProcess private constructor(
    private val process: Process,
    private val stdin: BufferedWriter,
    private val stdout: BufferedReader,
) {

    private val log = Logger.getInstance(R1McpBridgeProcess::class.java)

    fun isAlive(): Boolean = process.isAlive

    /**
     * Sends a single JSON-RPC frame to the child. The caller passes
     * already-serialised JSON; we append the framing newline.
     */
    @Synchronized
    fun send(frame: String) {
        if (!process.isAlive) {
            throw IOException("r1 mcp serve process is not alive")
        }
        stdin.write(frame)
        if (!frame.endsWith("\n")) {
            stdin.newLine()
        }
        stdin.flush()
    }

    /**
     * Reads the next JSON-RPC frame from the child. Returns null
     * when the child closes stdout (i.e. on shutdown).
     */
    fun recv(): String? {
        return stdout.readLine()
    }

    /**
     * SIGTERMs the child process and waits up to 5 seconds for
     * shutdown; on timeout we SIGKILL.
     */
    fun terminate() {
        try {
            stdin.close()
        } catch (_: IOException) {
            // ignore — closing on SIGTERM may race the child exit.
        }
        process.destroy()
        if (!process.waitFor(5, TimeUnit.SECONDS)) {
            log.warn("r1 mcp serve did not exit within 5s; force-killing")
            process.destroyForcibly()
        }
    }

    companion object {
        /**
         * Spawns `r1 mcp serve`. The child inherits the IDE's
         * environment but redirects stderr to the IDE log directory
         * so JSON-RPC traffic on stdout stays uncontaminated.
         */
        fun spawn(): R1McpBridgeProcess {
            val pb = ProcessBuilder("r1", "mcp", "serve")
            pb.redirectErrorStream(false)
            // Inherit stderr into the parent IDE log; stdin / stdout
            // stay piped so we own the framing.
            pb.redirectError(ProcessBuilder.Redirect.INHERIT)
            val p = pb.start()
            val stdin = BufferedWriter(
                OutputStreamWriter(p.outputStream, StandardCharsets.UTF_8))
            val stdout = BufferedReader(
                InputStreamReader(p.inputStream, StandardCharsets.UTF_8))
            return R1McpBridgeProcess(p, stdin, stdout)
        }

        // parseFrame / serializeFrame are exposed for FramingTest.kt
        // (spec §T6.2 JVM tests). They keep the framing rules in one
        // place so the bridge stays consistent across send/recv.

        /**
         * Validates a frame's framing rules. Returns the trimmed
         * payload on success or throws on protocol violation.
         */
        @JvmStatic
        fun parseFrame(input: String): String {
            if (input.isEmpty()) throw IOException("empty frame")
            // MCP stdio framing forbids embedded newlines inside a
            // frame: each frame is exactly one line.
            if (input.indexOf('\n') >= 0 &&
                input.indexOf('\n') != input.length - 1) {
                throw IOException("frame contains embedded newline")
            }
            val trimmed = input.trimEnd('\n', '\r')
            if (trimmed.length > MAX_FRAME_BYTES) {
                throw IOException("frame exceeds ${MAX_FRAME_BYTES}B")
            }
            // Must look like a JSON envelope.
            if (!trimmed.startsWith("{") || !trimmed.endsWith("}")) {
                throw IOException("frame is not a JSON object")
            }
            return trimmed
        }

        /** Serialises a payload to wire format (newline-terminated). */
        @JvmStatic
        fun serializeFrame(payload: String): String {
            require(payload.indexOf('\n') < 0) { "payload must not contain newlines" }
            require(payload.length <= MAX_FRAME_BYTES) {
                "payload exceeds ${MAX_FRAME_BYTES}B"
            }
            return "$payload\n"
        }

        // MCP doesn't specify a hard ceiling, but 16 MiB is a sensible
        // cap that comfortably accommodates tool-list responses while
        // catching runaway protocol bugs early.
        private const val MAX_FRAME_BYTES = 16 * 1024 * 1024
    }
}
