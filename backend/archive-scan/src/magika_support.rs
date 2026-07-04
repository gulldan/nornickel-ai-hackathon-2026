use std::{
    cell::RefCell,
    panic::{self, AssertUnwindSafe},
    sync::atomic::{AtomicBool, Ordering},
};

const MIN_MAGIKA_BYTES: usize = 256;

#[derive(Default)]
enum SessionState {
    #[default]
    Uninit,
    Ready(magika::Session),
    Disabled,
}

thread_local! {
    static MAGIKA_SESSION: RefCell<SessionState> = const { RefCell::new(SessionState::Uninit) };
}

static MAGIKA_DISABLED: AtomicBool = AtomicBool::new(false);

#[must_use]
pub const fn min_magika_bytes() -> usize {
    MIN_MAGIKA_BYTES
}

fn disable_magika(err: impl std::fmt::Display) {
    if !MAGIKA_DISABLED.swap(true, Ordering::Relaxed) {
        eprintln!("Magika disabled: {err}");
    }
}

fn init_session() -> Option<magika::Session> {
    match panic::catch_unwind(AssertUnwindSafe(magika::Session::new)) {
        Ok(Ok(session)) => Some(session),
        Ok(Err(err)) => {
            disable_magika(err);
            None
        }
        Err(_) => {
            disable_magika("panic during ONNX Runtime initialization");
            None
        }
    }
}

pub fn identify(bytes: &[u8]) -> Option<(String, String, f32)> {
    if bytes.len() < MIN_MAGIKA_BYTES || MAGIKA_DISABLED.load(Ordering::Relaxed) {
        return None;
    }

    MAGIKA_SESSION.with(|cell| {
        let mut state = cell.borrow_mut();

        if matches!(&*state, SessionState::Uninit) {
            *state = init_session().map_or_else(|| SessionState::Disabled, SessionState::Ready);
        }

        match &mut *state {
            SessionState::Ready(session) => {
                match panic::catch_unwind(AssertUnwindSafe(|| session.identify_content_sync(bytes)))
                {
                    Ok(Ok(result)) => {
                        let info = result.info();
                        Some((info.label.to_owned(), info.mime_type.to_owned(), result.score()))
                    }
                    Ok(Err(err)) => {
                        *state = SessionState::Disabled;
                        disable_magika(err);
                        None
                    }
                    Err(_) => {
                        *state = SessionState::Disabled;
                        disable_magika("panic during Magika identify");
                        None
                    }
                }
            }
            SessionState::Disabled | SessionState::Uninit => None,
        }
    })
}

#[cfg(test)]
pub(crate) fn set_disabled_for_tests(disabled: bool) {
    MAGIKA_DISABLED.store(disabled, Ordering::Relaxed);
    MAGIKA_SESSION.with(|cell| {
        *cell.borrow_mut() = if disabled { SessionState::Disabled } else { SessionState::Uninit };
    });
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::Mutex;

    static TEST_LOCK: Mutex<()> = Mutex::new(());

    #[test]
    fn min_magika_bytes_matches_short_circuit_threshold() {
        assert_eq!(min_magika_bytes(), 256);
    }

    #[test]
    fn identify_short_circuits_on_small_buffers() {
        assert!(identify(&vec![0_u8; MIN_MAGIKA_BYTES - 1]).is_none());
    }

    #[test]
    fn disable_magika_marks_global_state() {
        let _guard = TEST_LOCK.lock().expect("test lock should not be poisoned");
        set_disabled_for_tests(false);
        disable_magika("test");

        assert!(MAGIKA_DISABLED.load(Ordering::Relaxed));

        set_disabled_for_tests(false);
    }

    #[test]
    fn identify_short_circuits_when_globally_disabled() {
        let _guard = TEST_LOCK.lock().expect("test lock should not be poisoned");
        set_disabled_for_tests(true);
        assert!(identify(&vec![0_u8; MIN_MAGIKA_BYTES]).is_none());
        set_disabled_for_tests(false);
    }

    #[test]
    fn identify_respects_thread_local_disabled_state() {
        let _guard = TEST_LOCK.lock().expect("test lock should not be poisoned");
        set_disabled_for_tests(false);
        MAGIKA_SESSION.with(|cell| *cell.borrow_mut() = SessionState::Disabled);

        assert!(identify(&vec![0_u8; MIN_MAGIKA_BYTES]).is_none());

        set_disabled_for_tests(false);
    }
}
