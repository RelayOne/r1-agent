// build.gradle.kts — Kotlin Gradle build for the R1 Agent IntelliJ
// Platform plugin. Targets the IntelliJ Platform 2024.1+ (244 build).
//
// Build:    ./gradlew buildPlugin
// Test:     ./gradlew test
// Verify:   ./gradlew verifyPlugin
//
// First run downloads the IntelliJ Platform SDK (~1.5 GB) into
// ~/.gradle/caches; subsequent runs are incremental.

plugins {
    id("java")
    id("org.jetbrains.kotlin.jvm") version "1.9.25"
    id("org.jetbrains.intellij.platform") version "2.0.1"
}

group = "com.relayone"
version = "0.1.0"

repositories {
    mavenCentral()
    intellijPlatform {
        defaultRepositories()
    }
}

dependencies {
    intellijPlatform {
        intellijIdeaCommunity("2024.1.7")
        instrumentationTools()
        // junit-jupiter test framework lives in the Platform SDK; we
        // only need to declare the IntelliJ test framework module.
        testFramework(org.jetbrains.intellij.platform.gradle.TestFrameworkType.Platform)
    }

    testImplementation("junit:junit:4.13.2")
    testImplementation("org.opentest4j:opentest4j:1.3.0")
}

intellijPlatform {
    pluginConfiguration {
        ideaVersion {
            sinceBuild = "241"
            untilBuild = "243.*"
        }
    }
}

java {
    toolchain {
        languageVersion = JavaLanguageVersion.of(17)
    }
}

kotlin {
    jvmToolchain(17)
}

tasks {
    wrapper {
        gradleVersion = "8.10"
    }
    test {
        useJUnit()
    }
}
