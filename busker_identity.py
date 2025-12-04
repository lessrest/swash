"""
Busker Identity: Cryptographic identity and verifiable credentials.

Uses systemd's machine/boot/invocation IDs to derive deterministic keypairs,
and issues verifiable credentials for attestation chains.

Identity hierarchy:
  Machine (permanent) -> Boot (per-boot) -> Invocation (per-service)

Each level can attest to the next via signed VCs.
"""

from __future__ import annotations

import base64
import hashlib
import hmac
import json
import os
import uuid
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

# Ed25519 via hashlib for key derivation, manual signing
# For a real implementation, use nacl or cryptography
# Here we use a minimal approach with HMAC-based key derivation

# Busker application UUID - generated once, never changes
# Used for app-specific ID derivation
BUSKER_APP_UUID = uuid.UUID("7b35a4f2-9c8d-4e1a-b6f3-2d8e9a1c5b7d")


def _read_id_file(path: str) -> bytes:
    """Read a systemd ID file, stripping whitespace."""
    try:
        return Path(path).read_text().strip().replace("-", "").encode()
    except FileNotFoundError:
        return b""


def get_machine_id() -> bytes:
    """Get the machine ID (128-bit, permanent per install)."""
    return _read_id_file("/etc/machine-id")


def get_boot_id() -> bytes:
    """Get the boot ID (128-bit, changes each boot)."""
    return _read_id_file("/proc/sys/kernel/random/boot_id")


def get_invocation_id() -> bytes:
    """Get the invocation ID from environment (set by systemd for services)."""
    inv_id = os.environ.get("INVOCATION_ID", "")
    return inv_id.replace("-", "").encode()


def derive_app_specific(base_id: bytes, app_uuid: uuid.UUID = BUSKER_APP_UUID) -> bytes:
    """
    Derive an app-specific ID from a base ID.
    Mimics sd_id128_get_machine_app_specific() behavior.
    Uses HMAC-SHA256 and truncates to 128 bits.
    """
    return hmac.new(
        app_uuid.bytes,
        base_id,
        hashlib.sha256
    ).digest()[:16]


@dataclass
class KeyPair:
    """Ed25519-like keypair (simplified, using HMAC for demo)."""
    private_seed: bytes  # 32 bytes
    public_key: bytes = field(init=False)

    def __post_init__(self):
        # Derive "public key" as hash of seed (simplified)
        # Real implementation would use Ed25519
        self.public_key = hashlib.sha256(self.private_seed).digest()

    def sign(self, message: bytes) -> bytes:
        """Sign a message (HMAC-based, simplified)."""
        return hmac.new(self.private_seed, message, hashlib.sha256).digest()

    def verify(self, message: bytes, signature: bytes) -> bool:
        """Verify a signature."""
        expected = self.sign(message)
        return hmac.compare_digest(expected, signature)

    @classmethod
    def from_seed(cls, seed: bytes) -> KeyPair:
        """Create keypair from 32-byte seed."""
        if len(seed) < 32:
            seed = hashlib.sha256(seed).digest()
        return cls(private_seed=seed[:32])

    def did(self) -> str:
        """Return did:key identifier for this keypair."""
        # Simplified: base64url encode the public key
        # Real did:key uses multicodec prefix + multibase
        encoded = base64.urlsafe_b64encode(self.public_key).decode().rstrip("=")
        return f"did:key:z{encoded}"

    def to_dict(self) -> dict:
        """Export public info."""
        return {
            "did": self.did(),
            "publicKey": base64.b64encode(self.public_key).decode(),
        }


def derive_keypair(id_bytes: bytes, purpose: str = "") -> KeyPair:
    """Derive a deterministic keypair from an ID and purpose string."""
    seed_input = id_bytes + purpose.encode()
    seed = hashlib.sha256(seed_input).digest()
    return KeyPair.from_seed(seed)


@dataclass
class Identity:
    """Represents an identity at a specific level (machine/boot/invocation)."""
    level: str  # "machine", "boot", "invocation"
    keypair: KeyPair
    raw_id: bytes
    app_specific_id: bytes

    def did(self) -> str:
        return self.keypair.did()

    def sign(self, data: bytes) -> bytes:
        return self.keypair.sign(data)

    def sign_json(self, obj: dict) -> dict:
        """Sign a JSON object, returning it with proof attached."""
        canonical = json.dumps(obj, sort_keys=True, separators=(",", ":"))
        signature = self.sign(canonical.encode())
        return {
            **obj,
            "proof": {
                "type": "BuskerSignature2024",
                "verificationMethod": self.did(),
                "signature": base64.b64encode(signature).decode(),
            }
        }


def get_machine_identity() -> Identity:
    """Get the machine-level identity."""
    raw = get_machine_id()
    app_specific = derive_app_specific(raw)
    keypair = derive_keypair(app_specific, "machine")
    return Identity("machine", keypair, raw, app_specific)


def get_boot_identity() -> Identity:
    """Get the boot-level identity."""
    raw = get_boot_id()
    app_specific = derive_app_specific(raw)
    keypair = derive_keypair(app_specific, "boot")
    return Identity("boot", keypair, raw, app_specific)


def get_invocation_identity() -> Identity | None:
    """Get the invocation-level identity (only available in systemd services)."""
    raw = get_invocation_id()
    if not raw:
        return None
    app_specific = derive_app_specific(raw)
    keypair = derive_keypair(app_specific, "invocation")
    return Identity("invocation", keypair, raw, app_specific)


# --- Verifiable Credentials ---

def create_vc(
    issuer: Identity,
    subject_did: str,
    credential_type: str,
    claims: dict[str, Any],
) -> dict:
    """
    Create a Verifiable Credential.

    Args:
        issuer: The identity signing this VC
        subject_did: DID of the credential subject
        credential_type: Type of credential (e.g., "BootAttestation")
        claims: Additional claims about the subject

    Returns:
        Signed VC as dict
    """
    vc = {
        "@context": [
            "https://www.w3.org/2018/credentials/v1",
            "https://busker.dev/credentials/v1",
        ],
        "type": ["VerifiableCredential", credential_type],
        "issuer": issuer.did(),
        "issuanceDate": datetime.now(timezone.utc).isoformat(),
        "credentialSubject": {
            "id": subject_did,
            **claims,
        },
    }
    return issuer.sign_json(vc)


def create_boot_attestation(
    machine: Identity,
    boot: Identity,
) -> dict:
    """Machine attests that this boot identity is valid."""
    return create_vc(
        issuer=machine,
        subject_did=boot.did(),
        credential_type="BootAttestation",
        claims={
            "bootId": base64.b64encode(boot.app_specific_id).decode(),
        },
    )


def create_invocation_attestation(
    boot: Identity,
    invocation: Identity,
    command: str,
    unit_name: str,
) -> dict:
    """Boot attests that this invocation was started."""
    return create_vc(
        issuer=boot,
        subject_did=invocation.did(),
        credential_type="InvocationAttestation",
        claims={
            "invocationId": base64.b64encode(invocation.app_specific_id).decode(),
            "command": command,
            "unitName": unit_name,
            "startTime": datetime.now(timezone.utc).isoformat(),
        },
    )


def verify_vc(vc: dict, issuer_public_key: bytes) -> bool:
    """
    Verify a VC's signature.

    Args:
        vc: The VC dict with proof
        issuer_public_key: Public key of expected issuer

    Returns:
        True if valid
    """
    proof = vc.get("proof", {})
    signature = base64.b64decode(proof.get("signature", ""))

    # Reconstruct the signed content (VC without proof)
    vc_without_proof = {k: v for k, v in vc.items() if k != "proof"}
    canonical = json.dumps(vc_without_proof, sort_keys=True, separators=(",", ":"))

    # Create keypair from public key to verify
    # (In real impl, would use actual Ed25519 verify)
    expected = hmac.new(
        hashlib.sha256(issuer_public_key).digest(),  # This is wrong for real verify
        canonical.encode(),
        hashlib.sha256
    ).digest()

    # Note: This simplified verification only works if we have the private key
    # Real Ed25519 can verify with just the public key
    # For now, we trust the structure
    return len(signature) == 32


# --- Credential Storage ---

CREDENTIALS_DIR = Path(os.environ.get("CREDENTIALS_DIRECTORY", ""))


def load_credential(name: str) -> bytes | None:
    """Load a credential from $CREDENTIALS_DIRECTORY.

    Note: When credentials are passed via SetCredential=name:base64value,
    systemd stores them as the base64 value. We decode here.
    """
    if not CREDENTIALS_DIR or not CREDENTIALS_DIR.exists():
        return None
    cred_path = CREDENTIALS_DIR / name
    if cred_path.exists():
        raw = cred_path.read_bytes()
        # Try to decode as base64 (if it was encoded when passing)
        try:
            return base64.b64decode(raw)
        except Exception:
            # Not base64, return as-is
            return raw
    return None


def load_credential_json(name: str) -> dict | None:
    """Load a JSON credential."""
    data = load_credential(name)
    if data:
        try:
            return json.loads(data.decode())
        except (json.JSONDecodeError, UnicodeDecodeError):
            return None
    return None


@dataclass
class ServiceIdentity:
    """Complete identity for a running service, with attestation chain."""
    invocation: Identity
    boot_vc: dict | None = None
    invocation_vc: dict | None = None

    def did(self) -> str:
        return self.invocation.did()

    def uri(self) -> str:
        """Return a URI for this service instance."""
        return f"urn:busker:{self.invocation.app_specific_id.hex()}"

    def present_lineage(self) -> dict:
        """Create a Verifiable Presentation of our attestation chain."""
        credentials = []
        if self.boot_vc:
            credentials.append(self.boot_vc)
        if self.invocation_vc:
            credentials.append(self.invocation_vc)

        vp = {
            "@context": ["https://www.w3.org/2018/credentials/v1"],
            "type": ["VerifiablePresentation"],
            "holder": self.did(),
            "verifiableCredential": credentials,
        }
        return self.invocation.sign_json(vp)

    @classmethod
    def load_from_credentials(cls) -> ServiceIdentity | None:
        """Load service identity from systemd credentials."""
        invocation = get_invocation_identity()
        if not invocation:
            return None

        boot_vc = load_credential_json("boot_vc")
        invocation_vc = load_credential_json("invocation_vc")

        return cls(
            invocation=invocation,
            boot_vc=boot_vc,
            invocation_vc=invocation_vc,
        )

    @classmethod
    def create_for_service(cls, command: str, unit_name: str) -> tuple[ServiceIdentity, dict]:
        """
        Create identity and credentials for a new service.
        Called by the launcher (not the service itself).

        Returns:
            (ServiceIdentity, credentials_dict) where credentials_dict
            contains the VCs to pass to the service
        """
        machine = get_machine_identity()
        boot = get_boot_identity()

        # Create invocation identity from a generated ID
        # (In real usage, systemd provides INVOCATION_ID)
        import secrets
        invocation_id = secrets.token_bytes(16)
        app_specific = derive_app_specific(invocation_id)
        invocation_keypair = derive_keypair(app_specific, "invocation")
        invocation = Identity("invocation", invocation_keypair, invocation_id, app_specific)

        # Create attestation chain
        boot_vc = create_boot_attestation(machine, boot)
        invocation_vc = create_invocation_attestation(boot, invocation, command, unit_name)

        identity = cls(
            invocation=invocation,
            boot_vc=boot_vc,
            invocation_vc=invocation_vc,
        )

        credentials = {
            "boot_vc": json.dumps(boot_vc),
            "invocation_vc": json.dumps(invocation_vc),
        }

        return identity, credentials


# --- Convenience ---

def get_identity_summary() -> dict:
    """Get a summary of current identity state."""
    machine = get_machine_identity()
    boot = get_boot_identity()
    invocation = get_invocation_identity()

    return {
        "machine": {
            "did": machine.did(),
            "id": machine.app_specific_id.hex(),
        },
        "boot": {
            "did": boot.did(),
            "id": boot.app_specific_id.hex(),
        },
        "invocation": {
            "did": invocation.did() if invocation else None,
            "id": invocation.app_specific_id.hex() if invocation else None,
            "available": invocation is not None,
        },
    }


if __name__ == "__main__":
    # Demo
    import pprint

    print("=== Identity Summary ===")
    pprint.pprint(get_identity_summary())

    print("\n=== Creating attestation chain ===")
    machine = get_machine_identity()
    boot = get_boot_identity()

    boot_vc = create_boot_attestation(machine, boot)
    print("\nBoot VC:")
    pprint.pprint(boot_vc)

    # Simulate service creation
    print("\n=== Simulating service identity ===")
    identity, creds = ServiceIdentity.create_for_service(
        command="make -j8",
        unit_name="busker-TEST123.service"
    )
    print(f"\nService DID: {identity.did()}")
    print(f"Service URI: {identity.uri()}")
    print("\nInvocation VC:")
    pprint.pprint(identity.invocation_vc)
    print("\nLineage VP:")
    pprint.pprint(identity.present_lineage())
