package activation

import "crypto/ed25519"

// DefaultPublicKey is the Ed25519 public key used to verify activation keys.
//
// IMPORTANT: Replace this value with your own public key before shipping.
// The corresponding private key must be kept offline and used only with the
// `activationgen` tool to issue activation keys.
var DefaultPublicKey ed25519.PublicKey = ed25519.PublicKey{
  0xe9,
  0xe3,
  0xfc,
  0xe5,
  0xd8,
  0xd2,
  0xee,
  0x47,
  0xb3,
  0x67,
  0x6f,
  0xa3,
  0xc5,
  0x57,
  0x5a,
  0x84,
  0x01,
  0x34,
  0x00,
  0xe4,
  0x80,
  0x5f,
  0xb4,
  0xe7,
  0x1a,
  0x76,
  0x61,
  0x94,
  0xeb,
  0x67,
  0xd8,
  0xa5,
}

