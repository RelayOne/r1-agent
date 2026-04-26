package com.relayone.r1

import com.fasterxml.jackson.annotation.JsonInclude
import com.fasterxml.jackson.annotation.JsonProperty
import com.fasterxml.jackson.databind.DeserializationFeature
import com.fasterxml.jackson.databind.ObjectMapper
import com.fasterxml.jackson.module.kotlin.jacksonObjectMapper
import java.io.IOException
import java.net.HttpURLConnection
import java.net.URI
import java.nio.charset.StandardCharsets

/**
 * Pure (no IntelliJ Platform deps) HTTP client for the r1-agent
 * agentserve API. Lives separately so unit tests can exercise it
 * without booting the IDE — see `src/test/kotlin/...DaemonClientTest`.
 *
 * Wire format: `ide/PROTOCOL.md`.
 */
class DaemonClient(
    private val baseUrl: String,
    private val apiKey: String?,
    private val timeoutMs: Int = DEFAULT_TIMEOUT_MS,
) {
    init {
        require(baseUrl.startsWith("http://", ignoreCase = true) ||
                baseUrl.startsWith("https://", ignoreCase = true)) {
            "daemon baseUrl must start with http:// or https:// (got $baseUrl)"
        }
    }

    private val normalizedBase = baseUrl.trimEnd('/')

    fun getCapabilities(): Capabilities = request("GET", "/api/capabilities", null, Capabilities::class.java)

    fun submitTask(body: TaskRequestBody): TaskState {
        require(body.taskType.isNotBlank()) { "submitTask: task_type is required" }
        require(!body.description.isNullOrBlank() || !body.query.isNullOrBlank()) {
            "submitTask: description or query is required"
        }
        return request("POST", "/api/task", body, TaskState::class.java)
    }

    fun getTask(id: String): TaskState {
        require(id.isNotBlank()) { "getTask: id is required" }
        return request("GET", "/api/task/${java.net.URLEncoder.encode(id, "UTF-8")}", null, TaskState::class.java)
    }

    fun cancelTask(id: String): TaskState {
        require(id.isNotBlank()) { "cancelTask: id is required" }
        return request("POST", "/api/task/${java.net.URLEncoder.encode(id, "UTF-8")}/cancel", null, TaskState::class.java)
    }

    private fun <T> request(method: String, path: String, body: Any?, into: Class<T>): T {
        val url = URI.create(normalizedBase + path).toURL()
        val conn = (url.openConnection() as HttpURLConnection).apply {
            requestMethod = method
            connectTimeout = timeoutMs
            readTimeout = timeoutMs
            setRequestProperty("Accept", "application/json")
            apiKey?.let { setRequestProperty(BEARER_HEADER, it) }
            if (body != null) {
                doOutput = true
                setRequestProperty("Content-Type", "application/json")
            }
        }
        try {
            if (body != null) {
                conn.outputStream.use { os ->
                    val bytes = MAPPER.writeValueAsBytes(body)
                    os.write(bytes)
                }
            }
            val status = conn.responseCode
            val raw = (if (status in 200..299) conn.inputStream else conn.errorStream)
                ?.use { it.readBytes().toString(StandardCharsets.UTF_8) }
                ?: ""
            if (status !in 200..299) {
                val errorMsg = parseErrorEnvelope(raw)
                throw DaemonHttpException("daemon $method $path returned HTTP $status${if (errorMsg != null) ": $errorMsg" else ""}", status, raw.take(400))
            }
            if (raw.isEmpty()) {
                throw DaemonHttpException("daemon returned empty body", status, "")
            }
            return MAPPER.readValue(raw, into)
        } catch (ioe: IOException) {
            throw DaemonHttpException("daemon I/O error: ${ioe.message}", -1, "")
        } finally {
            conn.disconnect()
        }
    }

    private fun parseErrorEnvelope(raw: String): String? = try {
        if (raw.isBlank()) null else MAPPER.readTree(raw).get("error")?.asText()
    } catch (_: Exception) {
        null
    }

    companion object {
        const val BEARER_HEADER = "X-Stoke-Bearer"
        const val DEFAULT_TIMEOUT_MS = 120_000

        // Shared mapper. snake_case fields map onto camelCase Kotlin
        // properties via @JsonProperty annotations on the data classes.
        internal val MAPPER: ObjectMapper = jacksonObjectMapper().apply {
            configure(DeserializationFeature.FAIL_ON_UNKNOWN_PROPERTIES, false)
            setSerializationInclusion(JsonInclude.Include.NON_NULL)
        }

        /**
         * Resolve the bearer token honoring (in order): explicit value,
         * then the R1_API_KEY env var. Returns null when neither is
         * set so the daemon's no-auth dev mode keeps working.
         */
        fun resolveApiKey(explicit: String?): String? {
            val trimmed = explicit?.trim().orEmpty()
            if (trimmed.isNotEmpty()) return trimmed
            val fromEnv = System.getenv("R1_API_KEY")?.trim().orEmpty()
            return fromEnv.ifEmpty { null }
        }
    }
}

class DaemonHttpException(
    message: String,
    val status: Int,
    val bodySnippet: String,
) : RuntimeException(message)

data class Capabilities(
    @JsonProperty("version") val version: String = "",
    @JsonProperty("task_types") val taskTypes: List<String> = emptyList(),
    @JsonProperty("budget_usd") val budgetUsd: Double = 0.0,
    @JsonProperty("requires_auth") val requiresAuth: Boolean = false,
)

data class TaskRequestBody(
    @JsonProperty("task_type") val taskType: String,
    @JsonProperty("description") val description: String? = null,
    @JsonProperty("query") val query: String? = null,
    @JsonProperty("spec") val spec: String? = null,
    @JsonProperty("budget") val budget: Double? = null,
    @JsonProperty("effort") val effort: String? = null,
    @JsonProperty("extra") val extra: Map<String, Any?>? = null,
)

data class TaskState(
    @JsonProperty("id") val id: String = "",
    @JsonProperty("status") val status: String = "",
    @JsonProperty("task_type") val taskType: String = "",
    @JsonProperty("created_at") val createdAt: String = "",
    @JsonProperty("started_at") val startedAt: String? = null,
    @JsonProperty("completed_at") val completedAt: String? = null,
    @JsonProperty("summary") val summary: String? = null,
    @JsonProperty("size") val size: Int? = null,
    @JsonProperty("error") val error: String? = null,
)
