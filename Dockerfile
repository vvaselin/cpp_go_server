# ベースとなるOSイメージを指定 (軽量なAlpine Linuxを採用)
FROM gcc:latest

# コンテナ内にソースコードを置くためのフォルダを作成
WORKDIR /usr/src/app