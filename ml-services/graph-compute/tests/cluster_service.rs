//! Stateless end-to-end test of [`ClusterService`] with a mock vector source
//! (no network): asserts the response shape and fingerprint determinism.

use std::future::Future;

use graph_compute::application::{ClusterService, DocumentRef, RunInput, VectorSource};
use graph_compute::domain::vector::ChunkPoint;
use graph_compute::domain::DomainConfig;
use graph_compute::infrastructure::leiden::LeidenDetector;

struct MockSource {
    points: Vec<ChunkPoint>,
}

impl VectorSource for MockSource {
    fn fetch(
        &self,
        _owner_id: &str,
        _collection: &str,
    ) -> impl Future<Output = anyhow::Result<Vec<ChunkPoint>>> + Send {
        let points = self.points.clone();
        async move { Ok(points) }
    }
}

fn chunk(id: &str, vector: &[f32]) -> ChunkPoint {
    ChunkPoint {
        id: format!("{id}-0").into(),
        document_id: id.into(),
        vector: vector.to_vec(),
        chunk_index: 0,
        text: format!(
            "Abstract for {id}. We present a method and report results across several \
             experiments. The findings show a consistent improvement over the baseline."
        )
        .into(),
        filename: Some(format!("{id}.pdf").into()),
    }
}

fn sample_input() -> (MockSource, RunInput) {
    // Two well-separated groups in 4-D space.
    let points = vec![
        chunk("a", &[1.0, 0.0, 0.0, 0.0]),
        chunk("b", &[0.95, 0.05, 0.0, 0.0]),
        chunk("c", &[0.9, 0.1, 0.0, 0.0]),
        chunk("d", &[0.0, 0.0, 1.0, 0.0]),
        chunk("e", &[0.0, 0.0, 0.95, 0.05]),
        chunk("f", &[0.0, 0.0, 0.9, 0.1]),
    ];
    let documents = ["a", "b", "c", "d", "e", "f"]
        .into_iter()
        .map(|id| DocumentRef { id: id.to_owned(), filename: format!("{id}.pdf") })
        .collect();
    let input = RunInput {
        owner_id: String::new(),
        collection: "documents".to_owned(),
        config: DomainConfig::default(),
        documents,
        previous_clusters: Vec::new(),
    };
    (MockSource { points }, input)
}

#[tokio::test]
async fn stateless_run_clusters_two_groups() {
    let (source, input) = sample_input();
    let service = ClusterService::new(source, LeidenDetector);

    let output = service.run(input).await.expect("run succeeds");
    assert_eq!(output.stats.document_count, 6);
    assert_eq!(output.clusters.len(), 2, "two well-separated groups → two clusters");

    for cluster in &output.clusters {
        assert_eq!(cluster.metrics.size, 3);
        assert_eq!(cluster.members.len(), 3);
        // Members are sorted document ids.
        let mut sorted = cluster.members.clone();
        sorted.sort();
        assert_eq!(cluster.members, sorted);
        assert_eq!(cluster.fingerprint.len(), 16);
        assert!(!cluster.representatives.is_empty());
        assert!(cluster.representatives.iter().all(|rep| rep.snippet.chars().count() <= 360));
        // No previous board → no lineage.
        assert!(cluster.lineage.is_none());
        assert!(cluster.metrics.avg_similarity > 0.5);
    }
    assert!(output.stats.edge_count > 0);
}

#[tokio::test]
async fn run_is_deterministic() {
    let (source_a, input_a) = sample_input();
    let (source_b, input_b) = sample_input();
    let service_a = ClusterService::new(source_a, LeidenDetector);
    let service_b = ClusterService::new(source_b, LeidenDetector);

    let out_a = service_a.run(input_a).await.expect("run a");
    let out_b = service_b.run(input_b).await.expect("run b");

    let fingerprints = |output: &graph_compute::domain::cluster::ClusterOutput| -> Vec<String> {
        let mut fps: Vec<String> = output.clusters.iter().map(|c| c.fingerprint.clone()).collect();
        fps.sort();
        fps
    };
    assert_eq!(fingerprints(&out_a), fingerprints(&out_b));
}

#[tokio::test]
async fn lineage_is_attached_when_previous_board_present() {
    let (source, mut input) = sample_input();
    // Previous board whose one cluster equals the future {a,b,c} group.
    input.previous_clusters = vec![graph_compute::domain::cluster::PreviousCluster {
        id: "prev-1".to_owned(),
        members: vec!["a".to_owned(), "b".to_owned(), "c".to_owned()],
    }];
    let service = ClusterService::new(source, LeidenDetector);
    let output = service.run(input).await.expect("run succeeds");

    let matched = output
        .clusters
        .iter()
        .filter_map(|c| c.lineage.as_ref())
        .any(|l| l.previous_cluster_id == "prev-1" && l.jaccard > 0.99);
    assert!(matched, "the {{a,b,c}} cluster should match prev-1 with jaccard ~1.0");
}
