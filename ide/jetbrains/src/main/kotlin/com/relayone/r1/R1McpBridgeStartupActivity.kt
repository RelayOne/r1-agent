package com.relayone.r1

// R1McpBridgeStartupActivity — invokes R1McpBridgePlugin.initialize()
// on first project open per the IntelliJ Platform startup lifecycle
// (the new ProjectActivity replaces the deprecated
// StartupActivity.Background since 2024.x).

import com.intellij.openapi.application.ApplicationManager
import com.intellij.openapi.project.Project
import com.intellij.openapi.startup.ProjectActivity

class R1McpBridgeStartupActivity : ProjectActivity {
    override suspend fun execute(project: Project) {
        val service = ApplicationManager.getApplication()
            .getService(R1McpBridgePlugin::class.java)
        service.initialize()
    }
}
