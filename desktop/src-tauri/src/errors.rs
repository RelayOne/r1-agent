// SPDX-License-Identifier: MIT
//
// Shared IPC error taxonomy.
//
// Lives in a standalone module so both the bin target (src/main.rs
// + src/ipc.rs) and the lib target (src/lib.rs + tests/*.rs) can
// reference the same types without duplicate imports. The full IPC
// command surface stays in src/ipc.rs (bin only) because most verbs
// reference `tauri::AppHandle` types that aren't constructible
// outside a real Tauri runtime.

use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct IpcError {
    pub code: i32,
    pub stoke_code: String,
    pub message: String,
}

impl IpcError {
    #[allow(dead_code)]
    pub fn not_implemented(method: &'static str) -> Self {
        Self {
            code: -32010,
            stoke_code: "not_implemented".to_string(),
            message: format!("{method}: not implemented"),
        }
    }

    pub fn not_found(what: impl std::fmt::Display) -> Self {
        Self {
            code: -32002,
            stoke_code: "not_found".to_string(),
            message: format!("not found: {what}"),
        }
    }

    pub fn internal(msg: impl std::fmt::Display) -> Self {
        Self {
            code: -32603,
            stoke_code: "internal".to_string(),
            message: msg.to_string(),
        }
    }
}

pub type IpcResult<T> = Result<T, IpcError>;

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn not_implemented_carries_method_name() {
        let err = IpcError::not_implemented("foo.bar");
        assert_eq!(err.code, -32010);
        assert_eq!(err.stoke_code, "not_implemented");
        assert!(err.message.contains("foo.bar"));
    }

    #[test]
    fn not_found_includes_subject() {
        let err = IpcError::not_found("session 123");
        assert_eq!(err.code, -32002);
        assert!(err.message.contains("session 123"));
    }

    #[test]
    fn internal_carries_arbitrary_message() {
        let err = IpcError::internal("kaboom");
        assert_eq!(err.code, -32603);
        assert!(err.message.contains("kaboom"));
    }
}
