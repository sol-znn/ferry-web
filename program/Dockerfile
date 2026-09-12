# Build image for the Solana program.
#
# `cargo-build-sbf` ships in the Agave release image, but that image has no host
# Rust: the tool shells out to `cargo metadata` before it does anything, and
# then wants `rustup` so it can register the SBF toolchain it downloads. So the
# two halves come from two images -- Rust from the official one, the Solana
# build tool from the Agave release -- rather than installing either from a
# script at build time.
#
# The Agave version is what picks the platform-tools, and platform-tools is what
# decides which Rust edition the on-chain crates may use. An older SDK does not
# merely lag: `solana-program 1.18` resolves transitive crates that now require
# edition 2024, which its own rustc cannot parse, so the build fails on
# dependencies that have nothing to do with this program. Staying current is the
# fix, not pinning the dependency tree back.
FROM anzaxyz/agave:v3.1.14 AS solana

FROM rust:1.91-slim-bookworm

# cargo-build-sbf downloads platform-tools -- its own rustc and LLVM for the SBF
# target -- on first use, and needs these to fetch and unpack it.
RUN apt-get update \
 && apt-get install -y --no-install-recommends bzip2 xz-utils ca-certificates \
 && rm -rf /var/lib/apt/lists/*

COPY --from=solana /usr/bin/cargo-build-sbf /usr/bin/cargo-build-sbf
COPY --from=solana /usr/bin/solana /usr/bin/solana
COPY --from=solana /usr/bin/solana-keygen /usr/bin/solana-keygen
# The SBF linker script and target spec. cargo-build-sbf resolves these
# relative to its own location, so the path has to be preserved, not just the
# files.
COPY --from=solana /usr/bin/platform-tools-sdk /usr/bin/platform-tools-sdk

WORKDIR /work
ENTRYPOINT ["cargo-build-sbf"]
