package com.relayone.r1

import com.sun.net.httpserver.HttpServer
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Before
import org.junit.Test
import java.net.InetSocketAddress
import java.nio.charset.StandardCharsets

/**
 * Pure unit tests for [DaemonClient]. Boots a local
 * com.sun.net.httpserver.HttpServer to exercise the wire format
 * end-to-end without requiring the IntelliJ Platform test framework
 * (those slower tests live in DaemonClientPlatformTest if added).
 */
class DaemonClientTest {

    private lateinit var server: HttpServer
    private var lastBody: String = ""
    private var lastBearer: String? = null
    private var lastPath: String = ""

    @Before
    fun setUp() {
        server = HttpServer.create(InetSocketAddress("127.0.0.1", 0), 0)
        server.executor = null
        server.createContext("/") { ex ->
            lastPath = ex.requestURI.path
            lastBearer = ex.requestHeaders.getFirst(DaemonClient.BEARER_HEADER)
            lastBody = ex.requestBody.use { it.readBytes().toString(StandardCharsets.UTF_8) }
            val (status, body) = handleRoute(ex.requestMethod, ex.requestURI.path, lastBody)
            val bytes = body.toByteArray(StandardCharsets.UTF_8)
            ex.responseHeaders.set("Content-Type", "application/json")
            ex.sendResponseHeaders(status, bytes.size.toLong())
            ex.responseBody.use { it.write(bytes) }
        }
        server.start()
    }

    @After
    fun tearDown() {
        server.stop(0)
    }

    private val baseUrl: String
        get() = "http://127.0.0.1:${server.address.port}"

    /** Per-test routing table. Tests override by reassignment. */
    private var routes: (String, String, String) -> Pair<Int, String> = { _, _, _ ->
        500 to """{"error":"no route"}"""
    }

    private fun handleRoute(method: String, path: String, body: String): Pair<Int, String> =
        routes(method, path, body)

    @Test
    fun `rejects baseUrl without scheme`() {
        try {
            DaemonClient(baseUrl = "127.0.0.1:7777", apiKey = null)
            fail("expected IllegalArgumentException")
        } catch (e: IllegalArgumentException) {
            assertTrue(e.message!!.contains("http://"))
        }
    }

    @Test
    fun `submitTask sends bearer header and body`() {
        routes = { _, path, _ ->
            assertEquals("/api/task", path)
            200 to """{"id":"t-1","status":"completed","task_type":"explain","created_at":"2026-04-26T00:00:00Z","summary":"ok"}"""
        }
        val client = DaemonClient(baseUrl, "secret-token-456")
        val state = client.submitTask(
            TaskRequestBody(taskType = "explain", description = "hello world")
        )
        assertEquals("t-1", state.id)
        assertEquals("completed", state.status)
        assertEquals("secret-token-456", lastBearer)
        assertTrue("body should mention task_type", lastBody.contains("\"task_type\""))
        assertTrue("body should mention description", lastBody.contains("\"description\""))
    }

    @Test
    fun `submitTask rejects empty description and query`() {
        val client = DaemonClient(baseUrl, null)
        try {
            client.submitTask(TaskRequestBody(taskType = "explain"))
            fail("expected IllegalArgumentException")
        } catch (e: IllegalArgumentException) {
            assertTrue(e.message!!.contains("description or query"))
        }
    }

    @Test
    fun `getCapabilities deserializes envelope`() {
        routes = { _, _, _ ->
            200 to """{"version":"0.1.0","task_types":["explain","research"],"budget_usd":2.5,"requires_auth":true}"""
        }
        val client = DaemonClient(baseUrl, null)
        val caps = client.getCapabilities()
        assertEquals("0.1.0", caps.version)
        assertEquals(listOf("explain", "research"), caps.taskTypes)
        assertEquals(true, caps.requiresAuth)
        assertEquals(2.5, caps.budgetUsd, 0.0001)
    }

    @Test
    fun `non-2xx surfaces error envelope`() {
        routes = { _, _, _ ->
            400 to """{"error":"task_type required"}"""
        }
        val client = DaemonClient(baseUrl, null)
        try {
            client.submitTask(TaskRequestBody(taskType = "explain", description = "x"))
            fail("expected DaemonHttpException")
        } catch (e: DaemonHttpException) {
            assertEquals(400, e.status)
            assertTrue(e.message!!.contains("task_type required"))
        }
    }

    @Test
    fun `cancelTask hits cancel sub-route`() {
        routes = { method, path, _ ->
            assertEquals("POST", method)
            assertEquals("/api/task/t-x/cancel", path)
            200 to """{"id":"t-x","status":"cancelled","task_type":"explain","created_at":"2026-04-26T00:00:00Z"}"""
        }
        val client = DaemonClient(baseUrl, null)
        val state = client.cancelTask("t-x")
        assertEquals("cancelled", state.status)
    }

    @Test
    fun `resolveApiKey prefers explicit then env then null`() {
        // explicit wins
        assertEquals("from-arg", DaemonClient.resolveApiKey("from-arg"))
        // blank explicit -> falls back to env (when present); we can
        // not safely set env vars from JVM, so just check empty input
        val out = DaemonClient.resolveApiKey("   ")
        // env may or may not be set in the test environment, but it
        // must NEVER return the blank input verbatim.
        assertTrue(out == null || out!!.isNotBlank())
    }

    @Test
    fun `getTask hits id route and parses state`() {
        routes = { _, path, _ ->
            assertEquals("/api/task/t-poll", path)
            200 to """{"id":"t-poll","status":"running","task_type":"explain","created_at":"2026-04-26T00:00:00Z"}"""
        }
        val client = DaemonClient(baseUrl, null)
        val state = client.getTask("t-poll")
        assertEquals("t-poll", state.id)
        assertEquals("running", state.status)
        assertNull(state.summary)
        assertNotNull(state.taskType)
    }
}
