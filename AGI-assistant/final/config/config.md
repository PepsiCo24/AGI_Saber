# api-key
97443eb3-4d60-4738-bf9d-e7bf364566d2

# 向量化模型：
curl https://ark.cn-beijing.volces.com/api/v3/embeddings/multimodal \
   -H "Content-Type: application/json" \
   -H "Authorization: Bearer $ARK_API_KEY" \
   -d '{
    "model": "ep-20260119190844-zzrq7",
    "input": [
        {
            "type":"text",
            "text":"天很蓝，海很深"
        },
        {    
            "type":"image_url",
            "image_url":{
                "url":"https://ark-project.tos-cn-beijing.volces.com/images/view.jpeg"
            }
        }
      ]
}'

# 对话模型：
curl https://ark.cn-beijing.volces.com/api/v3/responses \
-H "Authorization: Bearer $ARK_API_KEY" \
-H 'Content-Type: application/json' \
-d '{
    "model": "ep-20260430233018-tqktj",
    "input": [
        {
            "role": "user",
            "content": [
                {
                    "type": "input_image",
                    "image_url": "https://ark-project.tos-cn-beijing.volces.com/doc_image/ark_demo_img_1.png"
                },
                {
                    "type": "input_text",
                    "text": "你看见了什么？"
                }
            ]
        }
    ]
}'