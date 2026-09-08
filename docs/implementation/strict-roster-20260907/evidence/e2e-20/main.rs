use rebound_toolbox::security::strict_build::{verify_online_runtime, GAME_SHA256};
use std::fs;
use std::path::PathBuf;

fn main() {
    let root = PathBuf::from(std::env::args().nth(1).expect("fixture root"));
    fs::create_dir_all(&root).expect("fixture root");
    fs::write(root.join("Payload.dll"), b"synthetic strict payload").expect("payload");
    fs::write(
        root.join("ProjectBoundarySteam-Win64-Shipping.exe"),
        b"wrong game bytes",
    )
    .expect("game");

    let err = verify_online_runtime(&root)
        .expect_err("wrong game bytes must fail before online launch")
        .to_string();
    assert!(
        err.contains("strict_runtime_hash_mismatch: game"),
        "unexpected strict gate error: {err}"
    );

    let expected_game = root.join("ProjectBoundarySteam-Win64-Shipping.exe");
    fs::write(&expected_game, b"still not the pinned game").expect("wrong game replacement");
    let err = verify_online_runtime(&root)
        .expect_err("replaced game bytes must remain rejected")
        .to_string();
    assert!(err.contains("strict_runtime_hash_mismatch: game"));

    println!(
        "PASS: strict online gate rejected wrong game SHA before credentials or process launch (expected pinned game SHA prefix={})",
        &GAME_SHA256[..12]
    );
}
