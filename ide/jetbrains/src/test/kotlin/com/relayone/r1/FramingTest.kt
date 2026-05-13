package com.relayone.r1

// FramingTest — JVM unit tests for R1McpBridgeProcess.parseFrame /
// serializeFrame. Four cases per spec §T6.2: valid, truncated,
// oversize, malformed.

import org.junit.Assert.assertEquals
import org.junit.Assert.fail
import org.junit.Test
import java.io.IOException

class FramingTest {

    @Test
    fun parseFrame_valid_passes() {
        val frame = "{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}"
        val parsed = R1McpBridgeProcess.parseFrame(frame)
        assertEquals(frame, parsed)
    }

    @Test
    fun parseFrame_trailingNewline_trimmed() {
        val frame = "{\"id\":1}\n"
        val parsed = R1McpBridgeProcess.parseFrame(frame)
        assertEquals("{\"id\":1}", parsed)
    }

    @Test
    fun parseFrame_truncated_rejected() {
        val frame = "{\"id\":1" // missing closing brace
        try {
            R1McpBridgePocessParseFrameOrRethrow(frame)
            fail("expected IOException for truncated frame")
        } catch (e: IOException) {
            // ok: parseFrame rejects non-JSON-object framing.
        }
    }

    @Test
    fun parseFrame_oversize_rejected() {
        val payload = StringBuilder("{")
        // 16 MiB + 1 byte → above MAX_FRAME_BYTES
        repeat(16 * 1024 * 1024) { payload.append('a') }
        payload.append("}")
        try {
            R1McpBridgeProcess.parseFrame(payload.toString())
            fail("expected IOException for oversize frame")
        } catch (e: IOException) {
            // ok
        }
    }

    @Test
    fun parseFrame_malformed_rejected() {
        // Embedded newline mid-frame is a framing violation even if
        // both halves are valid JSON-object snippets.
        val frame = "{\"a\":1}\n{\"b\":2}"
        try {
            R1McpBridgeProcess.parseFrame(frame)
            fail("expected IOException for embedded newline")
        } catch (e: IOException) {
            // ok
        }
    }

    @Test
    fun serializeFrame_appendsNewline() {
        val out = R1McpBridgeProcess.serializeFrame("{\"id\":1}")
        assertEquals("{\"id\":1}\n", out)
    }

    @Test
    fun serializeFrame_rejectsEmbeddedNewline() {
        try {
            R1McpBridgeProcess.serializeFrame("{\"a\":1}\n{\"b\":2}")
            fail("expected exception for embedded newline")
        } catch (e: IllegalArgumentException) {
            // ok
        }
    }

    /**
     * Thin wrapper to keep the truncated test's expectation explicit:
     * truncated frames violate the JSON-object framing rule even
     * though they fit within the size cap. Wrapping in a tiny helper
     * documents the intent.
     */
    private fun R1McpBridgePocessParseFrameOrRethrow(input: String): String {
        return R1McpBridgeProcess.parseFrame(input)
    }
}
