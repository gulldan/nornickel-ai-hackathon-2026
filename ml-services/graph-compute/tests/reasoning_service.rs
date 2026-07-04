//! Stateless end-to-end tests of the graph-reasoning RPCs ([`ClusterService::rank`]
//! and [`ClusterService::paths`]) with a mock vector source (no network):
//! Personalized PageRank seeded in one theme keeps its mass there, and PathRAG
//! recovers the chain linking two themes through their mediator. Deterministic.

use std::future::Future;

use graph_compute::application::{ClusterService, PathInput, RankInput, VectorSource};
use graph_compute::domain::vector::ChunkPoint;
use graph_compute::domain::{ConfigOverrides, DomainConfig, PathConfig, RankConfig};
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

fn score_of(nodes: &[graph_compute::domain::RankedNode], id: &str) -> f64 {
    nodes.iter().find(|node| node.id == id).map_or(0.0, |node| node.score)
}

/// Two orthogonal themes that the kNN graph splits into disjoint components.
fn two_theme_source() -> MockSource {
    let points = vec![
        chunk("a1", &[1.0, 0.0, 0.0, 0.0]),
        chunk("a2", &[0.97, 0.03, 0.0, 0.0]),
        chunk("a3", &[0.95, 0.05, 0.0, 0.0]),
        chunk("b1", &[0.0, 0.0, 1.0, 0.0]),
        chunk("b2", &[0.0, 0.0, 0.97, 0.03]),
        chunk("b3", &[0.0, 0.0, 0.95, 0.05]),
    ];
    MockSource { points }
}

#[tokio::test]
async fn seeded_rank_keeps_mass_in_the_seeded_theme() {
    // k=2 mutual kNN → theme A and theme B are disjoint components; seeding A
    // therefore strands all PageRank mass on A's nodes.
    let config =
        DomainConfig::resolve(ConfigOverrides { knn_k: Some(2), ..ConfigOverrides::default() });
    let input = RankInput {
        owner_id: String::new(),
        collection: "documents".to_owned(),
        config,
        seed_ids: vec!["a1".to_owned()],
        rank: RankConfig::default(),
    };
    let service = ClusterService::new(two_theme_source(), LeidenDetector);

    let output = service.rank(input).await.expect("rank succeeds");
    assert_eq!(output.stats.document_count, 6);
    assert_eq!(output.nodes.len(), 6);

    let a_min = ["a1", "a2", "a3"]
        .into_iter()
        .map(|id| score_of(&output.nodes, id))
        .fold(f64::MAX, f64::min);
    let b_max =
        ["b1", "b2", "b3"].into_iter().map(|id| score_of(&output.nodes, id)).fold(0.0, f64::max);
    assert!(a_min > b_max, "every seeded-theme node outranks every other-theme node");
    assert!(b_max < 1e-9, "the unseeded, disconnected theme holds no mass");

    let top = &output.nodes[0].id;
    assert!(["a1", "a2", "a3"].contains(&top.as_str()), "the top node is in the seeded theme");
}

#[tokio::test]
async fn rank_is_deterministic() {
    let config =
        DomainConfig::resolve(ConfigOverrides { knn_k: Some(2), ..ConfigOverrides::default() });
    let make = || RankInput {
        owner_id: String::new(),
        collection: "documents".to_owned(),
        config: config.clone(),
        seed_ids: vec!["a1".to_owned()],
        rank: RankConfig::default(),
    };
    let service_a = ClusterService::new(two_theme_source(), LeidenDetector);
    let service_b = ClusterService::new(two_theme_source(), LeidenDetector);

    let out_a = service_a.rank(make()).await.expect("run a");
    let out_b = service_b.rank(make()).await.expect("run b");
    assert_eq!(out_a, out_b);
}

#[tokio::test]
async fn paths_recovers_the_chain_through_the_mediator() {
    // Two affine themes joined by a mediator document; union kNN keeps the
    // mediator linked to both, so a connecting chain a1 → med → b1 exists.
    let points = vec![
        chunk("a1", &[1.0, 0.5, 0.0, 0.0]),
        chunk("a2", &[1.0, 0.55, 0.0, 0.0]),
        chunk("a3", &[1.0, 0.6, 0.0, 0.0]),
        chunk("b1", &[0.5, 1.0, 0.0, 0.0]),
        chunk("b2", &[0.55, 1.0, 0.0, 0.0]),
        chunk("b3", &[0.6, 1.0, 0.0, 0.0]),
        chunk("med", &[0.8, 0.8, 0.0, 0.0]),
    ];
    let config = DomainConfig::resolve(ConfigOverrides {
        knn_k: Some(3),
        mutual_knn: Some(false),
        ..ConfigOverrides::default()
    });
    let input = PathInput {
        owner_id: String::new(),
        collection: "documents".to_owned(),
        config,
        source_ids: vec!["a1".to_owned()],
        target_ids: vec!["b1".to_owned()],
        paths: PathConfig::default(),
    };
    let service = ClusterService::new(MockSource { points }, LeidenDetector);

    let output = service.paths(input).await.expect("paths succeeds");
    assert_eq!(output.stats.document_count, 7);
    assert!(!output.paths.is_empty(), "a connecting chain between the themes is found");

    let chain = &output.paths[0];
    assert_eq!(chain.node_ids.first().map(String::as_str), Some("a1"));
    assert_eq!(chain.node_ids.last().map(String::as_str), Some("b1"));
    assert!(chain.node_ids.iter().any(|id| id == "med"), "the chain routes through the mediator");
    assert!(chain.score > 0.0 && chain.score <= 1.0);
}
