"""Independent encrypted OPC-UA peer for the Go connector integration test."""
import argparse
import asyncio
import logging
from pathlib import Path
from asyncua import Server, ua, uamethod
from cryptography import x509
from cryptography.hazmat.primitives import hashes

parser = argparse.ArgumentParser()
parser.add_argument("--port", type=int, required=True)
parser.add_argument("--certificate", required=True)
parser.add_argument("--key", required=True)
parser.add_argument("--client-certificate", required=True)
args = parser.parse_args()

async def main():
    server = Server()
    await server.init()
    await server.set_application_uri("urn:smartfactory:server")
    server.set_endpoint(f"opc.tcp://127.0.0.1:{args.port}")
    server.set_security_policy([ua.SecurityPolicyType.Basic256Sha256_SignAndEncrypt])
    await server.load_certificate(args.certificate)
    await server.load_private_key(args.key, format="pem")
    trusted = x509.load_pem_x509_certificate(Path(args.client_certificate).read_bytes())

    async def validate_client(certificate, application):
        if certificate.fingerprint(hashes.SHA256()) != trusted.fingerprint(hashes.SHA256()):
            raise ua.UaStatusCodeError(ua.StatusCodes.BadCertificateUntrusted)

    server.set_certificate_validator(validate_client)
    namespace = await server.register_namespace("urn:smartfactory:secure-method")
    device = await server.nodes.objects.add_object(ua.NodeId("gas", namespace), "gas")
    extractor = await device.add_variable(ua.NodeId("extractor", namespace), "extractor", False)
    await extractor.set_writable()

    @uamethod
    async def set_extractor(parent, enabled):
        if not isinstance(enabled, bool):
            raise ua.UaStatusCodeError(ua.StatusCodes.BadTypeMismatch)
        await extractor.write_value(enabled)
        return enabled

    await device.add_method(ua.NodeId("set_extractor", namespace), "set_extractor", set_extractor, [ua.VariantType.Boolean], [ua.VariantType.Boolean])
    async with server:
        print(f"READY namespace={namespace}", flush=True)
        await asyncio.Event().wait()

logging.basicConfig(level=logging.ERROR)
asyncio.run(main())
