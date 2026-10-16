#!/bin/bash
# tokenkey Edge Lightsail bootstrap — generated; do not hand-edit.
set -euo pipefail
exec > >(tee -a /var/log/tokenkey-lightsail-bootstrap.log) 2>&1
echo "LIGHTSAIL_BOOTSTRAP_START $(date -u +%FT%TZ)"

: "${EDGE_ID:?EDGE_ID required}"
: "${INSTANCE_NAME:?INSTANCE_NAME required}"
: "${API_DOMAIN:?API_DOMAIN required}"
: "${ACME_EMAIL:?ACME_EMAIL required}"
: "${MAIN_GATEWAY_ALLOWED_CIDR:?MAIN_GATEWAY_ALLOWED_CIDR required}"
: "${TOKENKEY_IMAGE:?TOKENKEY_IMAGE required}"
: "${LIGHTSAIL_REGION:?LIGHTSAIL_REGION required}"
: "${SSM_ACTIVATION_ID:?SSM_ACTIVATION_ID required}"
: "${SSM_ACTIVATION_CODE:?SSM_ACTIVATION_CODE required}"
: "${GHCR_PAT_SSM_NAME:=}"
: "${GHCR_PULL_USER:=}"
: "${ALLOW_SECRET_GENERATE:=false}"

case "${ALLOW_SECRET_GENERATE}" in
  true|false) ;;
  *) echo "BOOTSTRAP_FAIL: ALLOW_SECRET_GENERATE must be true or false" >&2; exit 1 ;;
esac

if command -v hostnamectl >/dev/null 2>&1; then
  hostnamectl set-hostname "${INSTANCE_NAME}" || true
else
  hostname "${INSTANCE_NAME}" 2>/dev/null || true
fi

export ADMIN_EMAIL="${ADMIN_EMAIL:-admin@${API_DOMAIN}}"
export TZ_VALUE="${TZ_VALUE:-UTC}"

yum -y update || dnf -y update || true
(yum -y install docker awscli openssl gzip tar || dnf -y install docker aws-cli openssl gzip tar) || true
systemctl enable --now docker || true
if ! command -v docker >/dev/null; then
  (amazon-linux-extras install docker -y || dnf -y install docker) || true
  systemctl enable --now docker || true
fi
if ! docker compose version >/dev/null 2>&1; then
  mkdir -p /usr/local/lib/docker/cli-plugins
  curl -fsSL "https://github.com/docker/compose/releases/download/v2.29.7/docker-compose-linux-$(uname -m)" \
    -o /usr/local/lib/docker/cli-plugins/docker-compose
  chmod +x /usr/local/lib/docker/cli-plugins/docker-compose
fi

SWAP_SIZE_GIB="${SWAP_SIZE_GIB:-2}"
if [ "${SWAP_SIZE_GIB}" -gt 0 ] && [ ! -f /swapfile ]; then
  fallocate -l "${SWAP_SIZE_GIB}G" /swapfile || dd if=/dev/zero of=/swapfile bs=1M count=$((SWAP_SIZE_GIB * 1024)) status=progress
  chmod 0600 /swapfile
  mkswap /swapfile
  swapon /swapfile
  grep -q '^/swapfile ' /etc/fstab 2>/dev/null || echo '/swapfile none swap sw 0 0' >> /etc/fstab
fi

if ! rpm -q amazon-ssm-agent >/dev/null 2>&1; then
  if ! yum -y install amazon-ssm-agent && ! dnf -y install amazon-ssm-agent; then
    echo "BOOTSTRAP_FAIL: cannot install amazon-ssm-agent" >&2
    exit 1
  fi
fi
systemctl enable amazon-ssm-agent
if ! /usr/bin/amazon-ssm-agent -register -y \
      -id "${SSM_ACTIVATION_ID}" \
      -code "${SSM_ACTIVATION_CODE}" \
      -region "${LIGHTSAIL_REGION}"; then
  echo "BOOTSTRAP_FAIL: amazon-ssm-agent -register failed (activation id/code/region mismatch?)" >&2
  exit 1
fi
systemctl restart amazon-ssm-agent
for i in 1 2 3 4 5 6; do
  if systemctl is-active --quiet amazon-ssm-agent; then break; fi
  echo "amazon-ssm-agent not active yet (try ${i}/6) — sleep 5s"
  sleep 5
  systemctl restart amazon-ssm-agent || true
done
if ! systemctl is-active --quiet amazon-ssm-agent; then
  echo "BOOTSTRAP_FAIL: amazon-ssm-agent failed to stay active after register" >&2
  exit 1
fi

mkdir -p /var/lib/tokenkey/caddy/data /var/lib/tokenkey/caddy/config
install -d -m 0755 -o 1000 -g 1000 /var/lib/tokenkey/app
COMPOSE_GZB64='H4sIAAAAAAACA9UZaXPb1vG7fsUO7ek0rSGSkmgrcGQHImERFa8AoK9MBoGIRxIxCNAAKJnNeMZO48Zu7DRukmniONMZ90qbxk46k6tR4v/SiBL1yX+h+3AQ4CFKifWlOkBgr7dvr7cLHoGlw/yZOQKydYmYq6QL3DkJJFdtkBQLWVXTuoxlGl3QzYZNHOcYtG19XXUJVCzHpaCkSDSdwont6I5LTBelaaqrQsfUiA3JddVOGvpa0qUrXCLdY6CaGqhQs1ot3WXaumkSDeTyKl9a5S8oQpFb4aFuWy1YyWfFWZR2uHuducKEqjCG1Wjgzlj42ShoBkDDnRKbhVccy2TqukEQZrVd3TIdFm8BWuoVxtF/TVhIpFOpVmIApMQIzCRmZhxir+s14nHUqDl9Vr2FFmYDyByjGmgG4mFqlumq+GArptpCkoFiHq1HglZ3Vdtl0cIG+oRxXKvdJpqHG+zoFxN2BNC2bDfQHoCBxGKKXUwloueFhXkW/33IumV0WiRGPubLpKdU0osTb9NJ4tbGgLa1rwQaMKx33ZcUDVTXG2zw6ZFrpE1MzVEsM9Q15AmfPbtqOnUeC4FPFM+Kgd1M4m5Y9qXYZgf2C1AzM6NiAycefXU4eNnTI8GsO+iyyx3dxjj/OZltzEKjWbNndSv5nLWBnj6VdDprc2pbZ5/DxDv1zNWpgeD7sWMYStsy9FqXBdXYULvO04bGEej9/u7OrZtbm+/vfnijd/3+9v3/PNm8h7Y1rC70X7+3ff9fW5v3ejduIh1IwkpVEtPQf/iP/uMPdz5+E6+9G3/FpEGdn2ze3vn0FiSbRDXcJuw8/hYyqfkfrl0PFtJNpW7ojaa7lILe229t37rT/+IzeFmzapewYlCdX0YR25/+ZfuPX9KVZF4swi9h6+s3dz96AOnFlAM7916Hhq3WCC08uqXB1uOP+o/CBXbfewySxEP/N9/1v7rbe3j7h2uvqe027HzyKKo0Ur4q58rnSoosFPlyVVYkPlsu5aSldCYF24/eDeT3PrsL8ykHFQqE73z7AcRUhf4b/+x99fnOex9QVVeFQgF2Nt/p/fbfuKbHQYkUT5bi68p6W/BwHUPHIjiIOtPyEmYQtI5VR19iccGfAbCp2toQkFxpWw4ZyuowqQ+QwmgXll6i7CPmum5bZgtrecQorQoVJcfJnJLNo9GW0gMMV5XLaDu5Wlly7Q6JOHjxLC8q+bIkL6Vmvd9RXKUsyktU21FEsZzjl46+GntiGZsYRHXI1VFaWaxKMp9TKmL5vMBLEdsIgkmfmJtNH6d6JNNzY2LOiOWSzJdySlUsRDLiUJaJmMRqKVQyvGUx2/BoQ+9EZNRgy5zE+1ZoB2fmONozRGZhfm4cVUVNcJUKClgRecl7ZgdH2ISlKpwknSuLuThTCGNPj4Hi5WmCtNxyiSvycVm55anLS1LBMwx2BeqaQcYJitx5pVzhSwpmW4m6aw8My2RSVyezC7kCP5k9wrBMOsYu8jlB8r1g04ZlBOHZ//j8iWdH4ZEthwFDseBhcssDImqhsbUr5XJBkYSLfCQrhFBV5xZGGYpCaXijk8DDu+RyFMsXOYGGcOyJZVStpZvPh36bNayaaowyxnY7DIjv9lfnvEop8jKSRQ/s6eh++MSz8HB2HANs2vcxTXIF5ueeGZbHn68IIk2SqhgIjUFwixG1XJYrCl/KihcqslAuKVjIl+gBPAalp/AY8EcpJl+kki+yTFXOxhXY7/iI9QN70eCWotg+Ast4ajOkXsfmDJXZABcrXYu4dhecpqpZG7Nhty29UED9W3jEOtg7mpapoxdPQpBqGqx1sReqqx3DnY0U5gt8kZfFCwonZvPCWR5twi0XeOrlPXEsU1eNeK0dpxT5FbTqRCE+Kh404zTL1eyqF0J7oaazV0T+jHB+IruPYpm2bWlJNCczMOc0gS9U+Sof5uc0NMsspp+d218UrUbLF2TvQNqXhmXm5zMZ7LynCqbk/Fm+JE8RPEJD68PCYubE8am+4ORsfu+9R2iWmctMlYTFYhVPzWy5Wprs2zgByyxMk3WmUJXyioDHr3iWK8TT64CkeIBMDSHMxwm5exC6eNXF2XkVnI5dxwaPybLQ0m3bsoFoDYI52lZrutsF7OUtoAGJ3WBnzYmyc4WT+XMcVopsns9VC0JpReFKcl4sV4QsrfBnhBVMJ7zJCgXPbqVsVRSxql3Ag0AUy2Ismw9NFp7v2MgNbVBySRt4WFMN1cSmuG5Ylv2Td7HMFbhSlrqt/DQbmChmrHKFQkt4rQjYvXKyckYQJVlBh8Z8G1t/P9Kh030SU6lckmSR54o/ZqXpTFghUsGq4+Nu2FhOHXf9aawbkHhd0IHpDzAe+6GS8wej7bce7P7h5vbtN3pv3w3nQBw71smTzZv0w8Th9MnmLRyqtr6+03/wiT83Qu+7z3vv3PnvtXfxb/f6496NO+DPn4H0tk0Yn3L3/S+3H37R//vf6DAaTGN0EPvdx+EMjOOX9woCdl/7c//7b7a++dMEdcIZzQfVmqR2afAKAcWw8GIiW8wljkFio0Fc+slc9q4yvWa82zK9JjWynjRxKqcPTdfFmSrp9VhNdA1LR5z4somXgkV000Vrqwad58Ke1NVbxOrg2hln4CvX1tG7MB8AvA1G0+Qc8s6MRkHwdmIATC8e6DXT0IzyFK8TDmWyPcD4OtjfABVCLhv7vVBqN7ROCz3lf+49+1ZW6IyxtO8KQzPaQWa2sWHsqWa22IS2/7w23t0eMMenpwoj5flCgSZBu6Hojk1UzECmCnsbAxgN9tL2p+ZJZnKe+GkSq3xBjviQg+VHND3+HySHv7EoSOk7dxx2IhanObhlSG1wf4qJdCEuMIxvIoYeDVhoGcZR1wkcT0Ea79U2PY68bwm6xBkA6k7XrAFB+q5DaicHAvU6vAiMCYmjwUSbLQgKV5Xz2PAn4KWT4DaJGS6LRM8n8CYI9rbqOBQ2xJc4CXU9kk+ukJrPt3dCTx7thqTuOfEfSprQBPFNWjN0L1swNA432pFuJq7rqJa+VuGXHGu2jg3rzP8Ae3QQdVkaAAA='
CADDY_GZB64='H4sIAAAAAAACA61VT28TRxQ/25/iKeEQKmyvcUAoVVVcx4ClkERxKsppGe88e6fZ3dnOzDoxVqQUQSkViKriUuihVSu1olILFVIpCULqV6mdwImvwJvd2Ikjinroxd557837+3u/mQYj1zBawx78/SdULzWhzjsITcPo17GyGuO81xYB5qehFsiEFxqRMJBo1IBRVyctbaAtFRzrV5cb7vzSxWpjcfOEPdYu1t06HRfoyCJODo71rdY9X12tX6pedqsLC0uX6vNurTG/slmEFQxYD2JmfA1MIUTSQJy0AuHZcIrCGUV6KSKjizYdVEZbFawuNAvVheXFglOGGRnB7Gzl+PtwYXV12UqEBi40awXIoYUeszeMj7AgOr7RTATkqy0UrrMggDXEWI/CxlIZOOOAF0iNvJjP9/M5DOnGZHn53DQwL0TXY+AbE+u5UsmeC5r6KKJOoeucLLJYFAM0GiNP9WJTlKpT4hTVM1L18pt5SiLtfRJro5CFoCMRx2he79zfvbW1+92t4d07g+0fh18+Gs9s7oxDye3dvw6tQHprMLzxePDs15cvvh083RpsP391487e899e79wm168+fzG8cQeu+DSIAOEslSrXkbvK9vwKDL+5DW2StRi5GRn1N0nx9fW97Se7X/2UuRz+vvPy5pPdnWt7P2//s3WNHO/e+4sCvrp5e/joMX0Pnn0xeErGDyFz8nrnQawkP4ARDB98P3j6bHj3ofVIduN6sxp2//hh794vo+JtjBmz5o6MjgNNQGEXlUaXHG/0jjSD1Ll2kGjfJZSg6rIACmWSTUPTJ0wRAHqAts2UHbQVah/S/FpSGgrB4jko+cgC44Ncjwg4iomoRKG5iFAT7KyrRUko1Vp0ETwpA06W6Qow0DRtqtG2ESM+RxIvEIRbqjKQjAMFRANSpW5aiU6dwqmNDQgT2iOLeJ5uQRLZyRi0Vz5LUBPQZzQiTDbzuM0nS9dNlBilfiAcN+GUPhAaEaJMDFSsLGi5tFUuTxQzglan4hySjq+X969zVDQLKNTOFWoyigi8Ft6N5QntJ4UVCkRS6CsMpUHXl9psHrE5J9U6Uxy5/fqPlstKGgl97fkY4r+bXSAn0B+5oqlGOt1ku5spQnJcsGDch7Q3Obv4LLAzLZ90JiWuICS7HhWs4fSsVXkyjGmW2nZMttskokibdosnaNAGS+FCSy85zkGzWYd05Q7m3bkqYjC4YUoE68gU9rdhpj5/vu4267WPVxqrl4shp1nnc2eJUQwRky3CEiWUCIdEKqX3xoJAdmQxjjpjQZt1BeVepB+b437PYOTqwxqjdtpxGiUDmMqo7wSEbKNAr8AHlfKpymnHcU6ACMPEWBqdokxMoLMs7Eg8SSeSlIsns78KaYTWCcWxRJg1/a3MaWeRcrNrp+N6PrEQRh0ctZRqThvmZu/CQeHd8qGiRUipHu5CV3CUR8+pQepygv7+B6e5ffjSKN/1xmXR38rBaRIiTHF6iPAmbxztBEEwlkRkUymlqfH7aZ880hklPKKQKZh1KhOO3hVrn2/cluRZUgQEV4urtBeOc/GjzIhAlupofWLaIG04fVnqlSpkBj7VMrIsQogOoLF4bilbjjeaF9NPbggAAA=='
PRUNE_B64='H4sIAAAAAAACA7VWbU8iSRD+3r+inHUdMM4A7ofNoUNiFD2iyxJ0L7enhjQzDfQx9Mx1Nyin/Per7pnBQV1z3mUNJnR3vTz1PFUVPmzVhlzUhlRNyAfoyblgcPbrcR80HasDuLz8AmEiGSR3gkUwYZL50GdjJpikmsGCUxjOeRx54Uj4auJjkKuv5+3uefv7wMQZnLfbvcHV0dklRGxE57FWoBNo1H1CFNPgsXkCKU/xjceEWOtu4Gw//CBK02vUVw5pd38bnHYu2huWvf63bntQPDW92oLKWsyHNZ1MmZiyZc1nYoHehI/gGrbAGwH6Fw4rB24PQE+YIAAsnCTgFI5eaojxxpNQejRNPUNOE2ZcKS7GsBGitbNv3O+5hjoZcYKEnCQa8wJXEFFN90AkGtSExbEJMUYeeSLguHPSV8i10JQLUCkNmQIqIggli5jQnMYKY83o8snIxAC1xNO9UYVGkIh4aUoAPqNjBpKNUDERMhAMo0QwNK8I5M9k6JM1cZ0vR2eGykq61JNEfALvGS+Hh27vO55dMpLJDFKqJ8grJkkTqaGHR5J/V0tFFjSeMwjAccgokRBz7CmEa8wq+O5TOV5cN26rvkTIA83udaXqqzTm2piqSrWJDAIg73ugWLoHIfLAI9NvgY3mp1RqbmirOIFTtdYoKdpaxtDRV1rytFKFAGFs1ulk0c1fAXQdv3BbW2DUmImKNaxCK4B9m8Ger+u3Jnz23Wvcbr5gwc6N45aylTNmZg3s5luSSi50noJYmkm16FHvbyh3uC2g6b27UzcDAJulevlKq/bbva/Bi3wfm7s4MyU4xsxAgMdHvHu6CF5ifTfSMJnHkZ0Q1FiZDk4TsE33TMVX0H/rdjvds0HnJHAs3igJp0yiEiploYYiMbRqEVvUxByHZ7+101jjKwfYrjzzxl3hPjz4HTNXq5W7joZSmeQKm59Fxm821cgvXk8ZS0cbN1rSFFw5yxdP5oMU4XdrvHJcaP/euSLkbsJjUzyOtCfNGB9gNQgxlwAvDP87O3YXcDFn+BZS5Ct/w+7bdQ9FIljL3a2ureDgAJiiobHWfMZ+XOcx5jbgsNI85H6JNhTeiuk2fvlc9+oN/FzV6037+cPFSnFyePQGi28E1hLnwESwc4H2H9WNzv+FcbLIncLb2cZMDomwVDiEw3VCo5MqNafn4S6aUW3z97GrFNeJXK5WTTxf0bEB9BoSuEEoj3BxPDi6uAiOwYgGngazLXOANxpLBm/a2GtIaJWEJaRpz7m4hayd08vghXuh9QD3/cgobhjcVP0Bb7Phf6Z8NpvCVrvu4cLuOqcoG8/N93XrF8OZcdoqgwbA/s7o3SjtXbUM/n0xU1wBQgf1dV2qNB8bkHPL7cpdCF5s4RVmRjzwInDBrWYVQLaqMh/TD2MzLA/ZD44CxhCRT7PEWzDG3QPe6f1fT622jl/qlP/P4c8c8/9YST5FcsbfmNJyWT9h8gjJl/s/WXEbmaQKAAA='
RESTORE_SECRETS_B64='H4sIAAAAAAACA7VXbVPbRhD+rl+xGBdwqCLboWkbV854EiWTloDHdkoyhHgOa2Vp0Bt3JxMCzm/v3kmyZQwx+VA+BO50u/vs7rPPXba3rExw6zyILYxncM6EbwiUYGKWQBqk6LEgNIx+b9B774ycgV2rGccfRv0PI/XXwHn77vjIrtVveifDcb56Year186b3ofD0WJ3Pq8ZvcPD45PxW+fIGfRGju2xUKBhXPlBiHAKtfp2DcyphCacdcBNDIAJE0j7rRoEMS0BTDNlnEUokTeggqp+06YQtQ4IP/AktKHTKc4nmUwz2YAS9cMnWRgmV+YUY+RMYgPuoJU8w9KqsHnSAJz4CdQ4CplwNNGd0j/xzBQ44SjFC8jiizi5ioHxaRZhLEEl091pdwC/BhIOmrkvFGxiuElM5ThVlbhZ5Davgf0dvlgyucD4Aq+tPRXk1kf5jYA2rFNmfmuaf5pn+5aQbIpNqxLfPGeTiyytw9kZ3N7CjYq0AfA5c2FRYw1VGeVYjXmBLi+mggbWk9L5RtfsXCRhJhHynlAY6QPHyyzg6K5WpQM6lBmraDmFKNpjAxH/yO80SOIfuDdmLAxc6vQ4txt7RMO9hi5SmExYqPHZin20Q1g8hUXtKSSws0N7W2AeruwSPHKV8Rha2qhW32NXF7DrHL0m3CkPiAFHA9gnjs93l5YNIj5ewrM1DzkQajtMkoxsCXOGlEWKE4nuOMR4Kn065yVcnwpi6B8PR28HznDc7w2HJ8eD1/D3yWg8dF4NnBGMjkf9sXP0avCpP6Kajv9xPhWjBnkEu0BsvrHBnCmfamToF6W3W2+Bbes4lH0ee6/ebEC3XNCnBiXXoly1t/19mEM19zyL9fQ1AE0tfWKe16O1Vg/IK/BIlGVUkZ0LyQnqr3eAthv34Qi8HEruz76noqRP0sf4biPsgz+IYiRqax+eH3TAC/IstW+dRjHbpzS/zPTO9vWcamLRie3yiKoEre+4vIduWj+ASFmWRpVi6ztYX/bWUrhdkuL2HlI06pZyXunxX/Zi1Sp7TFphtxYd1rOl5OMltfcFHaiWlqTD0HU1cUVAdA6nxRgtd/P6Grrda0O6crSQtc3CRvCEDOIp5DugPQWCRkbHKKWuFDtV0Lm6gvwocaH5vNmshl0IqYoD6uos4gALOTL3mpiHQuk9aZCfCFkrVbRpEA8MUljMh80NeExiW3VONAxiEvMwBNMFM4Lm73n03IqiS4xSZRxdqL8qn6ynpZvt7SfW/OlH/aM8IucJ1+V7lKE+XjFfLYMyptorVi7cEqxJiCzO0kJEqd2TJIpY7KoZFT6pMHQtF2dWnFFq7e5Oa9FmKL6b2cPuyWJprZgfFZr8ABrQo6idbz5KTZkbkrMUiiTA+fhupDnLrgS9Dor7pHodCRHBFOXySQKfDfWQKPtZvcNN8yqQvukSS65TqTzlZy8z5NfQLz08/VcLfPlsAYlfJXSXwNvdO9A3Dkph+OgxGQ7fF7cMzUbEQrpZIlybjnYxHdFsrbCVEfnZ2SlQueDxJFJIiLYhdWDKMSVBu4TdRaGOEvmGbgp3Fx4qSKniq884wrhlg3rLVURmc1WWLVZVCYRQQqKYXTwXVUPpS5xIYJn0Ex58Wy/abznPgMRx2VJa6kvKo+TuirT9i/gcqwz3khRjIULgepp8/ArtA3o0dO/1s5T2Hzl41n7QwT0Xwk96egQbN9a8fIm7S23luCLX5XvuWed/IOMyvHpceQEX9GLlySwQ1GxFTK0tjxkndSGA+o8Uuh1aeJmoXEM8kZo+JVkknQNzAq1m+2BNAauv8QN1kfwHEOWuo8ANAAA='
printf '%s' "$COMPOSE_GZB64" | base64 -d | gunzip > /var/lib/tokenkey/docker-compose.yml
printf '%s' "$CADDY_GZB64" | base64 -d | gunzip > /var/lib/tokenkey/caddy/Caddyfile.template
envsubst '${API_DOMAIN} ${ACME_EMAIL} ${MAIN_GATEWAY_ALLOWED_CIDR}' \
  < /var/lib/tokenkey/caddy/Caddyfile.template > /var/lib/tokenkey/caddy/Caddyfile

printf '%s' "$PRUNE_B64" | base64 -d | gunzip > /usr/local/bin/tokenkey-prune-ghcr-app-tags-core.sh
chmod +x /usr/local/bin/tokenkey-prune-ghcr-app-tags-core.sh

printf '%s' "$RESTORE_SECRETS_B64" | base64 -d | gunzip > /usr/local/bin/tokenkey-restore-edge-env-secrets.sh
chmod 0755 /usr/local/bin/tokenkey-restore-edge-env-secrets.sh

SECRET_FILE=/var/lib/tokenkey/.env.secret
restore_secret_args=(
  --parameter "/tokenkey/edge/${EDGE_ID}/stage0/env-secrets-backup" \
  --output "$SECRET_FILE"
)
if [ "${ALLOW_SECRET_GENERATE}" = true ]; then
  restore_secret_args+=(--allow-generate)
fi
AWS_REGION="${LIGHTSAIL_REGION}" /usr/local/bin/tokenkey-restore-edge-env-secrets.sh \
  "${restore_secret_args[@]}"
set -a; . "$SECRET_FILE"; set +a

cat > /var/lib/tokenkey/.env <<ENVEOF
API_DOMAIN=${API_DOMAIN}
SERVER_FRONTEND_URL=https://${API_DOMAIN}
ACME_EMAIL=${ACME_EMAIL}
TZ=${TZ_VALUE}
SERVER_MODE=release
RUN_MODE=standard
TOKENKEY_IMAGE=${TOKENKEY_IMAGE}
POSTGRES_USER=tokenkey
POSTGRES_PASSWORD=${POSTGRES_PASSWORD}
POSTGRES_DB=tokenkey
DATABASE_MAX_OPEN_CONNS=10
DATABASE_MAX_IDLE_CONNS=2
REDIS_PASSWORD=
REDIS_DB=0
REDIS_POOL_SIZE=64
REDIS_MIN_IDLE_CONNS=2
ADMIN_EMAIL=${ADMIN_EMAIL}
ADMIN_PASSWORD=
JWT_SECRET=${JWT_SECRET}
JWT_EXPIRE_HOUR=1
TOTP_ENCRYPTION_KEY=${TOTP_ENCRYPTION_KEY}
GATEWAY_SCHEDULING_ANTHROPIC_CONFIG_RECONCILER_BALANCE_FLOOR_ENABLED=true
ENVEOF
chmod 0600 /var/lib/tokenkey/.env

if [ -n "${GHCR_PAT_SSM_NAME:-}" ]; then
  GHCR_PAT="$(aws --region "${LIGHTSAIL_REGION}" ssm get-parameter \
    --name "${GHCR_PAT_SSM_NAME}" --with-decryption \
    --query Parameter.Value --output text)"
  echo "${GHCR_PAT}" | docker login ghcr.io -u "${GHCR_PULL_USER}" --password-stdin
  unset GHCR_PAT
else
  echo "GHCR_PAT_SSM_NAME unset; relying on anonymous pull for public image ${TOKENKEY_IMAGE}"
fi

cat > /etc/systemd/system/tokenkey.service <<'UNITEOF'
[Unit]
Description=tokenkey edge lightsail stack (docker compose)
Requires=docker.service
After=docker.service network-online.target
Wants=network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
WorkingDirectory=/var/lib/tokenkey
EnvironmentFile=/var/lib/tokenkey/.env
ExecStartPre=/usr/bin/docker compose --env-file /var/lib/tokenkey/.env pull
ExecStart=/usr/bin/docker compose --env-file /var/lib/tokenkey/.env up -d --remove-orphans
ExecStop=/usr/bin/docker compose --env-file /var/lib/tokenkey/.env down
TimeoutStartSec=10min

[Install]
WantedBy=multi-user.target
UNITEOF

systemctl daemon-reload
systemctl enable --now tokenkey.service
sleep 30
docker compose -f /var/lib/tokenkey/docker-compose.yml --env-file /var/lib/tokenkey/.env ps || true
echo "LIGHTSAIL_BOOTSTRAP_DONE $(date -u +%FT%TZ)"
