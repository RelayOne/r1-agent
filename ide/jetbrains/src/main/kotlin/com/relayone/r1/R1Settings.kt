package com.relayone.r1

import com.intellij.openapi.application.ApplicationManager
import com.intellij.openapi.components.PersistentStateComponent
import com.intellij.openapi.components.Service
import com.intellij.openapi.components.State
import com.intellij.openapi.components.Storage
import com.intellij.util.xmlb.XmlSerializerUtil

/**
 * Application-level settings persisted via [PersistentStateComponent].
 * Mirrors the VS Code extension's settings:
 *   r1.daemonUrl, r1.apiKey, r1.taskType, r1.timeoutMs
 *
 * Storage is the standard XML state file: `r1-agent.xml` under the
 * IDE's options directory. Tests can mutate via `loadState`.
 */
@Service(Service.Level.APP)
@State(
    name = "com.relayone.r1.R1Settings",
    storages = [Storage("r1-agent.xml")],
)
class R1Settings : PersistentStateComponent<R1Settings.State> {

    data class State(
        var daemonUrl: String = DEFAULT_URL,
        var apiKey: String = "",
        var taskType: String = DEFAULT_TASK_TYPE,
        var timeoutMs: Int = DaemonClient.DEFAULT_TIMEOUT_MS,
    )

    private var myState = State()

    override fun getState(): State = myState

    override fun loadState(state: State) {
        XmlSerializerUtil.copyBean(state, myState)
    }

    fun newClient(): DaemonClient = DaemonClient(
        baseUrl = myState.daemonUrl.ifBlank { DEFAULT_URL },
        apiKey = DaemonClient.resolveApiKey(myState.apiKey),
        timeoutMs = if (myState.timeoutMs > 0) myState.timeoutMs else DaemonClient.DEFAULT_TIMEOUT_MS,
    )

    fun effectiveTaskType(): String = myState.taskType.ifBlank { DEFAULT_TASK_TYPE }

    companion object {
        const val DEFAULT_URL = "http://127.0.0.1:7777"
        const val DEFAULT_TASK_TYPE = "explain"

        fun getInstance(): R1Settings = ApplicationManager.getApplication().getService(R1Settings::class.java)
    }
}
