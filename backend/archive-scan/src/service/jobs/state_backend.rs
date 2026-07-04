use super::PersistedJobStoreState;
use anyhow::{anyhow, Context, Result};
use std::{
    collections::HashMap,
    fs::{self, File, OpenOptions},
    io::Write,
    path::{Path, PathBuf},
    sync::{
        atomic::{AtomicBool, Ordering},
        Arc, Mutex,
    },
    thread,
    time::{Duration, Instant},
};
use tempfile::NamedTempFile;

#[derive(Clone)]
pub(super) struct InMemoryJobStoreBackend {
    state: Arc<Mutex<PersistedJobStoreState>>,
    cancellations: Arc<Mutex<HashMap<String, Arc<AtomicBool>>>>,
}

#[derive(Clone, Debug)]
pub(super) struct FilesystemJobStoreBackend {
    state_path: PathBuf,
    lock_path: PathBuf,
    lock_timeout: Duration,
    lock_retry_interval: Duration,
}

impl InMemoryJobStoreBackend {
    pub(super) fn new() -> Self {
        Self {
            state: Arc::new(Mutex::new(PersistedJobStoreState::default())),
            cancellations: Arc::new(Mutex::new(HashMap::new())),
        }
    }

    pub(super) fn with_locked_state<T>(
        &self,
        f: impl FnOnce(&mut PersistedJobStoreState) -> Result<(T, bool)>,
    ) -> Result<T> {
        let mut state = self.state.lock().expect("in-memory job store lock should not be poisoned");
        let (value, _dirty) = f(&mut state)?;
        Ok(value)
    }

    pub(super) fn cancellation(&self, job_id: &str) -> Option<Arc<AtomicBool>> {
        let cancellations =
            self.cancellations.lock().expect("in-memory cancellation lock should not be poisoned");
        cancellations.get(job_id).cloned()
    }

    pub(super) fn register_cancellation(&self, job_id: &str) {
        let mut cancellations =
            self.cancellations.lock().expect("in-memory cancellation lock should not be poisoned");
        cancellations.insert(job_id.to_owned(), Arc::new(AtomicBool::new(false)));
    }

    pub(super) fn mark_cancelled(&self, job_id: &str) {
        let cancellations =
            self.cancellations.lock().expect("in-memory cancellation lock should not be poisoned");
        if let Some(flag) = cancellations.get(job_id) {
            flag.store(true, Ordering::Relaxed);
        }
    }

    pub(super) fn unregister_cancellation(&self, job_id: &str) {
        let mut cancellations =
            self.cancellations.lock().expect("in-memory cancellation lock should not be poisoned");
        cancellations.remove(job_id);
    }
}

impl FilesystemJobStoreBackend {
    pub(super) fn new(
        state_path: PathBuf,
        lock_timeout: Duration,
        lock_retry_interval: Duration,
    ) -> Self {
        let lock_path = lock_path_for(&state_path);
        Self { state_path, lock_path, lock_timeout, lock_retry_interval }
    }

    pub(super) fn with_locked_state<T>(
        &self,
        f: impl FnOnce(&mut PersistedJobStoreState) -> Result<(T, bool)>,
    ) -> Result<T> {
        let guard =
            FileLockGuard::acquire(&self.lock_path, self.lock_timeout, self.lock_retry_interval)?;
        let mut state = self.load_state()?;
        let (value, dirty) = f(&mut state)?;
        if dirty {
            self.save_state(&state)?;
        }
        drop(guard);
        Ok(value)
    }

    pub(super) fn readiness_check(&self) -> Result<()> {
        let parent = parent_dir(&self.state_path)?;
        fs::create_dir_all(parent).with_context(|| {
            format!("failed to create job store directory {}", parent.display())
        })?;
        let _guard =
            FileLockGuard::acquire(&self.lock_path, self.lock_timeout, self.lock_retry_interval)?;
        let _ = self.load_state()?;
        Ok(())
    }

    fn load_state(&self) -> Result<PersistedJobStoreState> {
        if !self.state_path.exists() {
            return Ok(PersistedJobStoreState::default());
        }
        let bytes = fs::read(&self.state_path).with_context(|| {
            format!("failed to read job store state {}", self.state_path.display())
        })?;
        serde_json::from_slice(&bytes).with_context(|| {
            format!("failed to deserialize job store state {}", self.state_path.display())
        })
    }

    fn save_state(&self, state: &PersistedJobStoreState) -> Result<()> {
        let parent = parent_dir(&self.state_path)?;
        fs::create_dir_all(parent).with_context(|| {
            format!("failed to create job store directory {}", parent.display())
        })?;
        let mut staging_file = NamedTempFile::new_in(parent).with_context(|| {
            format!("failed to allocate temp job store file in {}", parent.display())
        })?;
        serde_json::to_writer(staging_file.as_file_mut(), state)
            .context("failed to serialize filesystem job store state")?;
        staging_file.as_file_mut().flush().context("failed to flush filesystem job store state")?;
        staging_file.persist(&self.state_path).map_err(|err| {
            anyhow!("failed to persist job store state {}: {}", self.state_path.display(), err)
        })?;
        Ok(())
    }
}

struct FileLockGuard {
    _file: File,
}

impl FileLockGuard {
    fn acquire(path: &Path, timeout: Duration, retry: Duration) -> Result<Self> {
        let parent = parent_dir(path)?;
        fs::create_dir_all(parent)
            .with_context(|| format!("failed to create lock directory {}", parent.display()))?;
        let file = OpenOptions::new()
            .read(true)
            .write(true)
            .create(true)
            .truncate(false)
            .open(path)
            .with_context(|| format!("failed to open file lock {}", path.display()))?;
        let deadline = Instant::now() + timeout;
        loop {
            match file.try_lock() {
                Ok(()) => return Ok(Self { _file: file }),
                Err(fs::TryLockError::WouldBlock) => {
                    if Instant::now() >= deadline {
                        return Err(anyhow!(
                            "timed out waiting for filesystem job store lock {}",
                            path.display()
                        ));
                    }
                    thread::sleep(retry);
                }
                Err(fs::TryLockError::Error(err)) => {
                    return Err(err).with_context(|| {
                        format!("failed to acquire file lock {}", path.display())
                    });
                }
            }
        }
    }
}

fn lock_path_for(state_path: &Path) -> PathBuf {
    let mut file_name =
        state_path.file_name().and_then(|name| name.to_str()).unwrap_or("state").to_owned();
    file_name.push_str(".lock");
    state_path.with_file_name(file_name)
}

fn parent_dir(path: &Path) -> Result<&Path> {
    path.parent()
        .filter(|parent| !parent.as_os_str().is_empty())
        .ok_or_else(|| anyhow!("path {} does not have a parent directory", path.display()))
}
