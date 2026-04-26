package com.relayone.r1

import com.intellij.openapi.options.Configurable
import com.intellij.ui.components.JBLabel
import com.intellij.ui.components.JBPasswordField
import com.intellij.ui.components.JBTextField
import com.intellij.util.ui.FormBuilder
import javax.swing.JComponent
import javax.swing.JPanel

/**
 * Settings page registered under "Tools > R1 Agent" in the IDE
 * Settings dialog. Edits the application-level [R1Settings] state.
 */
class R1SettingsConfigurable : Configurable {
    private var panel: JPanel? = null
    private val daemonUrlField = JBTextField()
    private val apiKeyField = JBPasswordField()
    private val taskTypeField = JBTextField()
    private val timeoutMsField = JBTextField()

    override fun getDisplayName(): String = "R1 Agent"

    override fun createComponent(): JComponent {
        val s = R1Settings.getInstance().state
        daemonUrlField.text = s.daemonUrl
        apiKeyField.text = s.apiKey
        taskTypeField.text = s.taskType
        timeoutMsField.text = s.timeoutMs.toString()
        panel = FormBuilder.createFormBuilder()
            .addLabeledComponent(JBLabel("Daemon URL:"), daemonUrlField, 1, false)
            .addLabeledComponent(JBLabel("API key (X-Stoke-Bearer):"), apiKeyField, 1, false)
            .addLabeledComponent(JBLabel("Default task_type:"), taskTypeField, 1, false)
            .addLabeledComponent(JBLabel("Timeout (ms):"), timeoutMsField, 1, false)
            .addComponentFillVertically(JPanel(), 0)
            .panel
        return panel!!
    }

    override fun isModified(): Boolean {
        val s = R1Settings.getInstance().state
        return daemonUrlField.text != s.daemonUrl ||
                String(apiKeyField.password) != s.apiKey ||
                taskTypeField.text != s.taskType ||
                (timeoutMsField.text.toIntOrNull() ?: -1) != s.timeoutMs
    }

    override fun apply() {
        val s = R1Settings.getInstance().state
        s.daemonUrl = daemonUrlField.text.trim().ifBlank { R1Settings.DEFAULT_URL }
        s.apiKey = String(apiKeyField.password)
        s.taskType = taskTypeField.text.trim().ifBlank { R1Settings.DEFAULT_TASK_TYPE }
        s.timeoutMs = timeoutMsField.text.toIntOrNull()?.takeIf { it > 0 } ?: DaemonClient.DEFAULT_TIMEOUT_MS
    }

    override fun reset() {
        val s = R1Settings.getInstance().state
        daemonUrlField.text = s.daemonUrl
        apiKeyField.text = s.apiKey
        taskTypeField.text = s.taskType
        timeoutMsField.text = s.timeoutMs.toString()
    }

    override fun disposeUIResources() {
        panel = null
    }
}
