//! Stateless end-to-end test of [`ClusterService::score_bridges`] with a mock
//! vector source (no network): two affine-but-weakly-linked themes joined by a
//! mediator document should surface at least one scored bridge, deterministically.

use std::future::Future;

use graph_compute::application::{BridgeInput, ClusterService, DocumentRef, VectorSource};
use graph_compute::domain::bridge::BridgeConfig;
use graph_compute::domain::vector::ChunkPoint;
use graph_compute::domain::{ConfigOverrides, DomainConfig};
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

fn sample_input() -> (MockSource, BridgeInput) {
    // Two tight, affine themes (centroid cosine ≈ 0.88) plus a mediator document
    // sitting between them.
    let points = vec![
        chunk("a1", &[1.0, 0.5, 0.0, 0.0]),
        chunk("a2", &[1.0, 0.55, 0.0, 0.0]),
        chunk("a3", &[1.0, 0.6, 0.0, 0.0]),
        chunk("b1", &[0.5, 1.0, 0.0, 0.0]),
        chunk("b2", &[0.55, 1.0, 0.0, 0.0]),
        chunk("b3", &[0.6, 1.0, 0.0, 0.0]),
        chunk("med", &[0.8, 0.8, 0.0, 0.0]),
    ];
    let documents = ["a1", "a2", "a3", "b1", "b2", "b3", "med"]
        .into_iter()
        .map(|id| DocumentRef { id: id.to_owned(), filename: format!("{id}.pdf") })
        .collect();
    // Union kNN keeps the mediator linked to both themes; a low affinity floor
    // keeps the affine pair in play.
    let config = DomainConfig::resolve(ConfigOverrides {
        knn_k: Some(3),
        mutual_knn: Some(false),
        ..ConfigOverrides::default()
    });
    let bridge = BridgeConfig { min_affinity: 0.0, ..BridgeConfig::default() };
    let input = BridgeInput {
        owner_id: String::new(),
        collection: "documents".to_owned(),
        config,
        bridge,
        documents,
    };
    (MockSource { points }, input)
}

#[tokio::test]
async fn stateless_score_bridges_surfaces_a_bridge() {
    let (source, input) = sample_input();
    let service = ClusterService::new(source, LeidenDetector);

    let output = service.score_bridges(input).await.expect("score_bridges succeeds");
    assert_eq!(output.stats.document_count, 7);
    assert!(output.stats.edge_count > 0);
    assert!(!output.bridges.is_empty(), "an affine cross-community bridge is found");

    for bridge in &output.bridges {
        assert_ne!(bridge.community_a, bridge.community_b);
        assert_eq!(bridge.fingerprint.len(), 16);
        assert!(bridge.scores.affinity >= 0.0 && bridge.scores.affinity <= 1.0);
        assert!(bridge.scores.convergence >= 1, "bridges pass the convergence gate");
        assert!(!bridge.mediators.is_empty());
        let mut sorted_a = bridge.members_a.clone();
        sorted_a.sort();
        assert_eq!(bridge.members_a, sorted_a);
    }
}

#[tokio::test]
async fn score_bridges_is_deterministic() {
    let (source_a, input_a) = sample_input();
    let (source_b, input_b) = sample_input();
    let service_a = ClusterService::new(source_a, LeidenDetector);
    let service_b = ClusterService::new(source_b, LeidenDetector);

    let out_a = service_a.score_bridges(input_a).await.expect("run a");
    let out_b = service_b.score_bridges(input_b).await.expect("run b");

    let fingerprints = |output: &graph_compute::domain::bridge::BridgeOutput| -> Vec<String> {
        output.bridges.iter().map(|b| b.fingerprint.clone()).collect()
    };
    assert_eq!(fingerprints(&out_a), fingerprints(&out_b));
}
